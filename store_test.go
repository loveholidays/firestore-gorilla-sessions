// Copyright 2019 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firestoregorilla

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStore(t *testing.T) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT not set")
	}
	ctx := context.Background()

	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		t.Fatalf("firestore.NewClient: %v", err)
	}
	defer client.Close()

	s, err := New(ctx, client)

	r := httptest.NewRequest("GET", "/", nil)
	const name = "testname"
	session, err := s.New(r, name)
	if err != nil {
		t.Errorf("New: %v", err)
	}
	defer s.cleanup(name)

	session.Values["testkey"] = "testvalue"
	session.Values["expire"] = int(time.Now().Unix())

	rr := httptest.NewRecorder()
	if err := s.Save(r, rr, session); err != nil {
		t.Errorf("Save: %v", err)
	}

	got, err := s.Get(r, name)
	if err != nil {
		t.Errorf("Get: %v", err)
	}
	if !cmp.Equal(session.Values, got.Values) {
		t.Errorf("Get got a session with diff Values (-want, +got):\n%s", cmp.Diff(session.Values, got.Values))
	}
	if got.IsNew {
		t.Errorf("Get got IsNew=true, want false")
	}

	cachedSession, err := s.New(r, name)
	if err != nil {
		t.Fatalf("New cachedSession: %v", err)
	}
	if !cmp.Equal(session.Values, cachedSession.Values) {
		t.Errorf("Get got a session with diff Values (-want, +got):\n%s", cmp.Diff(session.Values, cachedSession.Values))
	}
	if cachedSession.IsNew {
		t.Errorf("New got cachedSession.IsNew=true, want false")
	}
}

func TestFirestoreErrorsPreserveCauseAndStatus(t *testing.T) {
	var cause error
	conn, err := grpc.Dial("localhost:1", grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(func(context.Context, string, interface{}, interface{}, *grpc.ClientConn, grpc.UnaryInvoker, ...grpc.CallOption) error {
			return cause
		}),
		grpc.WithStreamInterceptor(func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, grpc.Streamer, ...grpc.CallOption) (grpc.ClientStream, error) {
			return nil, cause
		}),
	)
	require.NoError(t, err)
	defer conn.Close()

	client, err := firestore.NewClient(context.Background(), "test-project", option.WithGRPCConn(conn))
	require.NoError(t, err)
	defer client.Close()

	store, err := New(context.Background(), client)
	require.NoError(t, err)

	const name = "testname"
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(name, "session-id")

	for _, code := range []codes.Code{codes.PermissionDenied, codes.Canceled} {
		cause = status.Error(code, "Firestore request failed")
		t.Run(code.String(), func(t *testing.T) {
			for _, tt := range []struct {
				name string
				call func() error
			}{
				{
					name: "Get",
					call: func() error {
						_, err := store.New(r, name)
						return err
					},
				},
				{
					name: "Create",
					call: func() error {
						session := sessions.NewSession(store, name)
						session.ID = "session-id"
						return store.Save(r, httptest.NewRecorder(), session)
					},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					err := tt.call()
					require.EqualError(t, err, tt.name+": "+cause.Error())
					require.ErrorIs(t, err, cause)
					require.Equal(t, code, status.Code(err))
				})
			}
		})
	}
}

func TestWrapFirestoreContextCancellation(t *testing.T) {
	err := wrapFirestoreError("Get", context.Canceled)
	require.EqualError(t, err, "Get: context canceled")
	require.ErrorIs(t, err, context.Canceled)
}

func TestMaxLength(t *testing.T) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT not set")
	}
	ctx := context.Background()

	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		t.Fatalf("firestore.NewClient: %v", err)
	}
	defer client.Close()

	s, err := New(ctx, client)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := &http.Request{}

	const name = "TestMaxLength"
	session, err := s.New(r, name)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.cleanup(name)

	if _, err := s.serialize(session); err != nil {
		t.Errorf("serialize(%+v) want nil error, got %v", session, err)
	}

	bigSession, err := s.New(r, name)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Ensure bigSession is over maxLength.
	bigSession.Values["store"] = strings.Repeat("firestore", 1<<20)

	sessionStr, err := s.serialize(bigSession)
	if err == nil {
		t.Fatalf("serialize(bigSession) want max length error, got nil error\n\tgot=%d bytes, maxLenth=%d bytes", len([]byte(sessionStr)), maxLength)
	}
	// Confirm the error was about the max length, not something else like gob
	// encoding.
	if want := "max length"; !strings.Contains(err.Error(), want) {
		t.Errorf("serialize(bigSession) got err %q, want to contain %q", err.Error(), want)
	}
}

// cleanup deletes every document in the name collection.
func (s *Store) cleanup(name string) {
	iter := s.client.Collection(name).DocumentRefs(context.Background())
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			// Ignore.
		}
		// Ignore errors.
		doc.Delete(context.Background())
	}
}

func Test_extractBookingIDs(t *testing.T) {
	for _, tt := range []struct {
		name          string
		session       *sessions.Session
		retBookingIDs []string
		retErr        bool
	}{
		{
			name:    "nil session",
			session: nil,
			retErr:  true,
		},
		{
			name: "no bookings IDs returns nil",
			session: &sessions.Session{
				ID: "some-session-id",
				Values: map[interface{}]interface{}{
					"data": "some-data",
				},
			},
			retBookingIDs: nil,
		},
		{
			name: "empty booking IDs slice is allowed",
			session: &sessions.Session{
				ID: "some-session-id",
				Values: map[interface{}]interface{}{
					"data":       "some-data",
					"bookingIds": []string{},
				},
			},
			retBookingIDs: []string{},
		},
		{
			name: "bookings IDs returned",
			session: &sessions.Session{
				ID: "some-session-id",
				Values: map[interface{}]interface{}{
					"data":       "some-data",
					"bookingIds": []string{"123456", "789012"},
				},
			},
			retBookingIDs: []string{"123456", "789012"},
		},
		{
			name: "error if booking IDs incorrect type",
			session: &sessions.Session{
				ID: "some-session-id",
				Values: map[interface{}]interface{}{
					"data":       "some-data",
					"bookingIds": 123456,
				},
			},
			retErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bookingIDs, err := extractBookingIDs(tt.session)
			require.Equal(t, bookingIDs, tt.retBookingIDs)
			if tt.retErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func Test_extractBookingIDsInterface(t *testing.T) {
	t.Run("some booking IDs", func(t *testing.T) {
		session := &sessions.Session{
			ID: "some-session-id",
			Values: map[interface{}]interface{}{
				"data":       "some-data",
				"bookingIds": []interface{}{"123456", "789012"},
			},
		}

		bookingIDs, err := extractBookingIDs(session)
		require.NoError(t, err)
		require.Equal(t, []string{"123456", "789012"}, bookingIDs)
	})

	t.Run("non-string booking ID", func(t *testing.T) {
		session := &sessions.Session{
			ID: "some-session-id",
			Values: map[interface{}]interface{}{
				"data":       "some-data",
				"bookingIds": []interface{}{"123456", 789012},
			},
		}

		bookingIDs, err := extractBookingIDs(session)
		require.Error(t, err)
		require.Nil(t, bookingIDs)
	})

	t.Run("no booking IDs", func(t *testing.T) {
		session := &sessions.Session{
			ID: "some-session-id",
			Values: map[interface{}]interface{}{
				"data":       "some-data",
				"bookingIds": []interface{}{},
			},
		}

		bookingIDs, err := extractBookingIDs(session)
		require.NoError(t, err)
		require.Nil(t, bookingIDs)
	})
}
