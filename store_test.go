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
	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/iterator"
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
		t.Errorf("serialize(bigSession) got retErr %q, want to contain %q", err.Error(), want)
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
		retBookingIDs BookingIDs
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
			name: "bookings IDs returned",
			session: &sessions.Session{
				ID: "some-session-id",
				Values: map[interface{}]interface{}{
					"data":       "some-data",
					"bookingIds": BookingIDs{"123456", "789012"},
				},
			},
			retBookingIDs: BookingIDs{"123456", "789012"},
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
