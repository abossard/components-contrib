/*
Copyright 2021 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdata "github.com/dapr/components-contrib/metadata"
	"github.com/dapr/components-contrib/pubsub"
	"github.com/dapr/kit/logger"
)

func newBroker() *rabbitMQInMemoryBroker {
	return &rabbitMQInMemoryBroker{
		buffer: make(chan amqp.Delivery, 2),
	}
}

func newRabbitMQTest(broker *rabbitMQInMemoryBroker) *rabbitMQ {
	return &rabbitMQ{
		declaredExchanges: make(map[string]bool),
		logger:            logger.NewLogger("test"),
		connectionDial: func(protocol, uri, clientName string, heartBeat time.Duration, tlsCfg *tls.Config, externalSasl bool) (rabbitMQConnectionBroker, rabbitMQChannelBroker, error) {
			broker.connectCount.Add(1)
			return broker, broker, nil
		},
		closeCh: make(chan struct{}),
	}
}

func TestNoConsumerOrQueueName(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey: "anyhost",
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)
	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{}, nil)
	assert.Contains(t, err.Error(), "consumerID is required for subscriptions that don't specify a queue name")
}

func TestPublishAndSubscribeWithPriorityQueue(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey:   "anyhost",
			metadataConsumerIDKey: "consumer",
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)
	assert.Equal(t, int32(1), broker.connectCount.Load())
	assert.Equal(t, int32(0), broker.closeCount.Load())

	topic := "mytopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return nil
	}

	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic, Metadata: map[string]string{metadataMaxPriority: "5"}}, handler)
	require.NoError(t, err)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("hello world"), Metadata: map[string]string{metadataMaxPriority: "5"}})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	// subscribe using classic queue type
	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic, Metadata: map[string]string{reqMetadataQueueTypeKey: "classic"}}, handler)
	require.NoError(t, err)

	// publish using classic queue type
	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("hey there"), Metadata: map[string]string{reqMetadataQueueTypeKey: "classic"}})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "hey there", lastMessage)

	// subscribe using quorum queue type
	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic, Metadata: map[string]string{reqMetadataQueueTypeKey: "quorum"}}, handler)
	require.NoError(t, err)

	// publish using quorum queue type
	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("hello friends"), Metadata: map[string]string{reqMetadataQueueTypeKey: "quorum"}})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 3, messageCount)
	assert.Equal(t, "hello friends", lastMessage)

	// trying to subscribe using invalid queue type
	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic, Metadata: map[string]string{reqMetadataQueueTypeKey: "invalid"}}, handler)
	require.Error(t, err)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 4, messageCount)
	assert.Equal(t, "foo bar", lastMessage)

	// subscribe using single active consumer
	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic, Metadata: map[string]string{reqMetadataSingleActiveConsumerKey: "true"}}, handler)
	require.NoError(t, err)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("dummy data"), Metadata: map[string]string{reqMetadataSingleActiveConsumerKey: "true"}})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 5, messageCount)
	assert.Equal(t, "dummy data", lastMessage)
}

func TestConcurrencyMode(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{Base: mdata.Base{
			Properties: map[string]string{
				metadataHostnameKey:   "anyhost",
				metadataConsumerIDKey: "consumer",
				pubsub.ConcurrencyKey: string(pubsub.Parallel),
			},
		}}
		err := pubsubRabbitMQ.Init(t.Context(), metadata)
		require.NoError(t, err)
		assert.Equal(t, pubsub.Parallel, pubsubRabbitMQ.metadata.Concurrency)
	})

	t.Run("single", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{Base: mdata.Base{
			Properties: map[string]string{
				metadataHostnameKey:   "anyhost",
				metadataConsumerIDKey: "consumer",
				pubsub.ConcurrencyKey: string(pubsub.Single),
			},
		}}
		err := pubsubRabbitMQ.Init(t.Context(), metadata)
		require.NoError(t, err)
		assert.Equal(t, pubsub.Single, pubsubRabbitMQ.metadata.Concurrency)
	})

	t.Run("default", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{Base: mdata.Base{
			Properties: map[string]string{
				metadataHostnameKey:   "anyhost",
				metadataConsumerIDKey: "consumer",
			},
		}}
		err := pubsubRabbitMQ.Init(t.Context(), metadata)
		require.NoError(t, err)
		assert.Equal(t, pubsub.Parallel, pubsubRabbitMQ.metadata.Concurrency)
	})
}

func TestPublishAndSubscribe(t *testing.T) {
	tests := []struct {
		name              string
		componentMetadata map[string]string
		subscribeMetadata map[string]string
		topic             string
		declaredQueues    []string
	}{
		{
			name: "only consumer id",
			componentMetadata: map[string]string{
				metadataHostnameKey:   "anyhost",
				metadataConsumerIDKey: "consumer",
			},
			topic:          "mytopic",
			declaredQueues: []string{"consumer-mytopic"},
		},
		{
			name: "only queue name",
			componentMetadata: map[string]string{
				metadataHostnameKey: "anyhost",
			},
			subscribeMetadata: map[string]string{
				metadataQueueNameKey: "myqueue",
			},
			topic:          "mytopic",
			declaredQueues: []string{"myqueue"},
		},
		{
			name: "queue name takes precedence over consumer id",
			componentMetadata: map[string]string{
				metadataHostnameKey:   "anyhost",
				metadataConsumerIDKey: "consumer",
			},
			subscribeMetadata: map[string]string{
				metadataQueueNameKey: "myqueue",
			},
			topic:          "mytopic",
			declaredQueues: []string{"myqueue"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newBroker()
			pubsubRabbitMQ := newRabbitMQTest(broker)
			metadata := pubsub.Metadata{Base: mdata.Base{
				Properties: test.componentMetadata,
			}}
			err := pubsubRabbitMQ.Init(t.Context(), metadata)
			require.NoError(t, err)
			assert.Equal(t, int32(1), broker.connectCount.Load())
			assert.Equal(t, int32(0), broker.closeCount.Load())

			messageCount := 0
			lastMessage := ""
			processed := make(chan bool)
			handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
				messageCount++
				lastMessage = string(msg.Data)
				processed <- true
				return nil
			}

			err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: test.topic, Metadata: test.subscribeMetadata}, handler)
			require.NoError(t, err)
			assert.True(t, pubsubRabbitMQ.declaredExchanges[test.topic])
			assert.ElementsMatch(t, test.declaredQueues, broker.declaredQueues)

			err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: test.topic, Data: []byte("hello world")})
			require.NoError(t, err)
			<-processed
			assert.Equal(t, 1, messageCount)
			assert.Equal(t, "hello world", lastMessage)

			err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: test.topic, Data: []byte("foo bar")})
			require.NoError(t, err)
			<-processed
			assert.Equal(t, 2, messageCount)
			assert.Equal(t, "foo bar", lastMessage)
		})
	}
}

func TestPublishReconnect(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey:   "anyhost",
			metadataConsumerIDKey: "consumer",
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)
	assert.Equal(t, int32(1), broker.connectCount.Load())
	assert.Equal(t, int32(0), broker.closeCount.Load())

	topic := "othertopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return nil
	}

	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic}, handler)
	require.NoError(t, err)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte(errorChannelConnection)})
	require.Error(t, err)
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)
	// Check that reconnection happened
	assert.Equal(t, int32(3), broker.connectCount.Load()) // three counts - one initial connection plus 2 reconnect attempts
	assert.Equal(t, int32(4), broker.closeCount.Load())   // four counts - one for connection, one for channel , times 2 reconnect attempts

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "foo bar", lastMessage)
}

func TestPublishReconnectAfterClose(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey:   "anyhost",
			metadataConsumerIDKey: "consumer",
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)
	assert.Equal(t, int32(1), broker.connectCount.Load())
	assert.Equal(t, int32(0), broker.closeCount.Load())

	topic := "mytopic2"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return nil
	}

	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic}, handler)
	require.NoError(t, err)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	require.NoError(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	// Close PubSub
	err = pubsubRabbitMQ.Close()
	require.NoError(t, err)
	assert.Equal(t, int32(2), broker.closeCount.Load()) // two counts - one for connection, one for channel

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte(errorChannelConnection)})
	require.Error(t, err)
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)
	// Check that reconnection did not happened
	assert.Equal(t, int32(1), broker.connectCount.Load())
	assert.Equal(t, int32(2), broker.closeCount.Load()) // two counts - one for connection, one for channel
}

func TestSubscribeBindRoutingKeys(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey:   "anyhost",
			metadataConsumerIDKey: "consumer",
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)
	assert.Equal(t, int32(1), broker.connectCount.Load())
	assert.Equal(t, int32(0), broker.closeCount.Load())

	topic := "mytopic_routingkeys"

	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		return nil
	}

	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic, Metadata: map[string]string{"routingKey": "keya,keyb,"}}, handler)
	require.NoError(t, err)
}

func TestSubscribeReconnect(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey:             "anyhost",
			metadataConsumerIDKey:           "consumer",
			metadataAutoAckKey:              "true",
			metadataReconnectWaitSecondsKey: "0",
			pubsub.ConcurrencyKey:           string(pubsub.Single),
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)
	assert.Equal(t, int32(1), broker.connectCount.Load())
	assert.Equal(t, int32(0), broker.closeCount.Load())

	topic := "thetopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return errors.New(errorChannelConnection)
	}

	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic}, handler)
	require.NoError(t, err)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	require.NoError(t, err)
	select {
	case <-processed:
	case <-time.After(5 * time.Second):
		require.Fail(t, "timeout waiting for message")
	}
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	require.NoError(t, err)
	select {
	case <-processed:
	case <-time.After(5 * time.Second):
		require.Fail(t, "timeout waiting for message")
	}
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "foo bar", lastMessage)

	// allow last reconnect completion
	time.Sleep(time.Second)

	// Check that reconnection happened
	assert.Equal(t, int32(3), broker.connectCount.Load()) // initial connect + 2 reconnects
	assert.Equal(t, int32(4), broker.closeCount.Load())   // two counts for each connection closure - one for connection, one for channel
}

func createAMQPMessage(body []byte) amqp.Delivery {
	return amqp.Delivery{Body: body}
}

type rabbitMQInMemoryBroker struct {
	buffer          chan amqp.Delivery
	declaredQueues  []string
	connectCount    atomic.Int32
	closeCount      atomic.Int32
	lastMsgMetadata *amqp.Publishing // Add this field to capture the last message metadata
}

func (r *rabbitMQInMemoryBroker) Qos(prefetchCount, prefetchSize int, global bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) PublishWithContext(ctx context.Context, exchange string, key string, mandatory bool, immediate bool, msg amqp.Publishing) error {
	// This is actually how the SDK implements it
	_, err := r.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, immediate, msg)
	return err
}

func (r *rabbitMQInMemoryBroker) PublishWithDeferredConfirmWithContext(ctx context.Context, exchange string, key string, mandatory bool, immediate bool, msg amqp.Publishing) (*amqp.DeferredConfirmation, error) {
	if string(msg.Body) == errorChannelConnection {
		return nil, errors.New(errorChannelConnection)
	}

	// Store the last message metadata for inspection in tests
	r.lastMsgMetadata = &msg

	// Use a non-blocking send or a separate goroutine to prevent deadlock
	// when there's no consumer reading from the buffer
	select {
	case r.buffer <- createAMQPMessage(msg.Body):
		// Message sent successfully
	default:
		// Buffer is full or there's no consumer, but we don't want to block
	}

	return nil, nil
}

func (r *rabbitMQInMemoryBroker) QueueDeclare(name string, durable bool, autoDelete bool, exclusive bool, noWait bool, args amqp.Table) (amqp.Queue, error) {
	r.declaredQueues = append(r.declaredQueues, name)
	return amqp.Queue{Name: name}, nil
}

func (r *rabbitMQInMemoryBroker) QueueBind(name string, key string, exchange string, noWait bool, args amqp.Table) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Consume(queue string, consumer string, autoAck bool, exclusive bool, noLocal bool, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	return r.buffer, nil
}

func (r *rabbitMQInMemoryBroker) Nack(tag uint64, multiple bool, requeue bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Ack(tag uint64, multiple bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) ExchangeDeclare(name string, kind string, durable bool, autoDelete bool, internal bool, noWait bool, args amqp.Table) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Confirm(noWait bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Close() error {
	r.closeCount.Add(1)

	return nil
}

func (r *rabbitMQInMemoryBroker) IsClosed() bool {
	return r.connectCount.Load() <= r.closeCount.Load()
}

// TestPublishMetadataProperties tests that message metadata properties are correctly passed to the broker
func TestPublishMetadataProperties(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{Base: mdata.Base{
		Properties: map[string]string{
			metadataHostnameKey:   "anyhost",
			metadataConsumerIDKey: "consumer",
		},
	}}
	err := pubsubRabbitMQ.Init(t.Context(), metadata)
	require.NoError(t, err)

	topic := "metadatatest"

	// Create a consumer for the test to prevent channel deadlock
	messageHandler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		return nil
	}
	err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topic}, messageHandler)
	require.NoError(t, err)

	// Test messageID
	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{
		Topic: topic,
		Data:  []byte("test message"),
		Metadata: map[string]string{
			"messageID": "msg-123",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "msg-123", broker.lastMsgMetadata.MessageId)

	// Test correlationID
	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{
		Topic: topic,
		Data:  []byte("test message"),
		Metadata: map[string]string{
			"correlationID": "corr-456",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "corr-456", broker.lastMsgMetadata.CorrelationId)

	// Test Type
	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{
		Topic: topic,
		Data:  []byte("test message"),
		Metadata: map[string]string{
			"type": "mytype",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "mytype", broker.lastMsgMetadata.Type)

	// Test all properties together
	err = pubsubRabbitMQ.Publish(t.Context(), &pubsub.PublishRequest{
		Topic: topic,
		Data:  []byte("test message"),
		Metadata: map[string]string{
			"messageID":     "msg-789",
			"correlationID": "corr-789",
			"type":          "complete-type",
			"contentType":   "application/json",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "msg-789", broker.lastMsgMetadata.MessageId)
	assert.Equal(t, "corr-789", broker.lastMsgMetadata.CorrelationId)
	assert.Equal(t, "complete-type", broker.lastMsgMetadata.Type)
	assert.Equal(t, "application/json", broker.lastMsgMetadata.ContentType)
}

func TestPublishMessagePropertiesToMetadataFlag(t *testing.T) {
	topicName := "test-topic"
	messageData := []byte("test message data")

	t.Run("flag is true", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{Base: mdata.Base{
			Properties: map[string]string{
				metadataHostnameKey:                           "anyhost",
				metadataConsumerIDKey:                         "consumer",
				metadataPublishMessagePropertiesToMetadataKey: "true",
			},
		}}
		err := pubsubRabbitMQ.Init(t.Context(), metadata)
		require.NoError(t, err)

		var receivedMsg *pubsub.NewMessage
		processed := make(chan bool)
		handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
			receivedMsg = msg
			processed <- true
			return nil
		}

		err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topicName}, handler)
		require.NoError(t, err)

		// Publish a message with some AMQP properties
		broker.buffer <- amqp.Delivery{
			Body:        messageData,
			MessageId:   "msg-id-true",
			ContentType: "text/plain",
			Headers: amqp.Table{
				"customHeader": "customValue",
			},
		}

		<-processed
		require.NotNil(t, receivedMsg)
		assert.Equal(t, messageData, receivedMsg.Data)
		assert.Equal(t, topicName, receivedMsg.Topic)
		assert.Equal(t, "msg-id-true", receivedMsg.Metadata["metadata.messageid"])
		assert.Equal(t, "text/plain", receivedMsg.Metadata["metadata.contenttype"])
		assert.Equal(t, "customValue", receivedMsg.Metadata["metadata.customHeader"])
	})

	t.Run("flag is false", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{Base: mdata.Base{
			Properties: map[string]string{
				metadataHostnameKey:                           "anyhost",
				metadataConsumerIDKey:                         "consumer",
				metadataPublishMessagePropertiesToMetadataKey: "false", // Explicitly false
			},
		}}
		err := pubsubRabbitMQ.Init(t.Context(), metadata)
		require.NoError(t, err)

		var receivedMsg *pubsub.NewMessage
		processed := make(chan bool)
		handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
			receivedMsg = msg
			processed <- true
			return nil
		}

		err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topicName}, handler)
		require.NoError(t, err)

		// Publish a message with some AMQP properties
		broker.buffer <- amqp.Delivery{
			Body:        messageData,
			MessageId:   "msg-id-false",
			ContentType: "application/xml",
		}

		<-processed
		require.NotNil(t, receivedMsg)
		assert.Equal(t, messageData, receivedMsg.Data)
		assert.Equal(t, topicName, receivedMsg.Topic)
		assert.Empty(t, receivedMsg.Metadata, "Metadata should be empty when flag is false")
	})

	t.Run("flag is not set (default to false)", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{Base: mdata.Base{
			Properties: map[string]string{
				metadataHostnameKey:   "anyhost",
				metadataConsumerIDKey: "consumer",
				// metadataPublishMessagePropertiesToMetadataKey is not set
			},
		}}
		err := pubsubRabbitMQ.Init(t.Context(), metadata)
		require.NoError(t, err)

		var receivedMsg *pubsub.NewMessage
		processed := make(chan bool)
		handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
			receivedMsg = msg
			processed <- true
			return nil
		}

		err = pubsubRabbitMQ.Subscribe(t.Context(), pubsub.SubscribeRequest{Topic: topicName}, handler)
		require.NoError(t, err)

		// Publish a message with some AMQP properties
		broker.buffer <- amqp.Delivery{
			Body:        messageData,
			MessageId:   "msg-id-default",
			ContentType: "application/json",
		}

		<-processed
		require.NotNil(t, receivedMsg)
		assert.Equal(t, messageData, receivedMsg.Data)
		assert.Equal(t, topicName, receivedMsg.Topic)
		assert.Empty(t, receivedMsg.Metadata, "Metadata should be empty when flag is not set (defaults to false)")
	})
}
