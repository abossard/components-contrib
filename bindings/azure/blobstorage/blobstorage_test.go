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

package blobstorage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dapr/components-contrib/bindings"
	"github.com/dapr/kit/logger"
)

func TestGetOption(t *testing.T) {
	blobStorage := NewAzureBlobStorage(logger.NewLogger("test")).(*AzureBlobStorage)

	t.Run("return error if blobName is missing", func(t *testing.T) {
		r := bindings.InvokeRequest{}
		_, err := blobStorage.get(t.Context(), &r)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrMissingBlobName)
	})
}

func TestDeleteOption(t *testing.T) {
	blobStorage := NewAzureBlobStorage(logger.NewLogger("test")).(*AzureBlobStorage)

	t.Run("return error if blobName is missing", func(t *testing.T) {
		r := bindings.InvokeRequest{}
		_, err := blobStorage.delete(t.Context(), &r)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrMissingBlobName)
	})

	t.Run("return error for invalid deleteSnapshots", func(t *testing.T) {
		r := bindings.InvokeRequest{}
		r.Metadata = map[string]string{
			"blobName":        "foo",
			"deleteSnapshots": "invalid",
		}
		_, err := blobStorage.delete(t.Context(), &r)
		require.Error(t, err)
	})
}
