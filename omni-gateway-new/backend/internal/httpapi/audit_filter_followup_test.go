package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

type auditFilterFollowupStore struct {
	*adminTestStore
	filters []model.AuditFilter
}

func (s *auditFilterFollowupStore) ListAuditFiltered(_ context.Context, filter model.AuditFilter) ([]*model.AuditEntry, error) {
	s.filters = append(s.filters, filter)
	return nil, nil
}

func TestCoverageFollowupAuditFilterUTCInclusivePagingAndHTTPValidation(t *testing.T) {
	query := url.Values{
		"actor_id": {"17"},
		"action":   {"PATCH"},
		"from":     {"2026-10-03T03:00:00+02:00"},
		"to":       {"2026-10-03T03:10:00-05:00"},
		"limit":    {"25"},
		"offset":   {"50"},
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/audit?"+query.Encode(), nil)
	parsed, err := auditFilterFromQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 10, 3, 8, 10, 0, 0, time.UTC)
	if parsed.ActorID != 17 || parsed.Action != "PATCH" || !parsed.From.Equal(wantFrom) || parsed.From.Location() != time.UTC ||
		!parsed.To.Equal(wantTo) || parsed.To.Location() != time.UTC || parsed.Limit != 25 || parsed.Offset != 50 {
		t.Fatalf("parsed audit filter = %+v", parsed)
	}

	store := &auditFilterFollowupStore{adminTestStore: newAdminTestStore()}
	handler := NewHandlerWithGatewayAndCipher(nil, store, store, nil, AuthConfig{}, nil)
	cookie, _ := adminTestSession(t, handler)
	valid := httptest.NewRequest(http.MethodGet, "/api/admin/audit?"+query.Encode(), nil)
	valid.AddCookie(cookie)
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusOK || len(store.filters) != 1 || !reflect.DeepEqual(store.filters[0], parsed) {
		t.Fatalf("HTTP audit filter status=%d filters=%+v", validResponse.Code, store.filters)
	}

	for _, invalidQuery := range []string{
		"actor_id=-1",
		"actor_id=0",
		"actor_id=invalid",
		"from=not-a-time",
		"to=not-a-time",
		"from=2026-10-04T00%3A00%3A00Z&to=2026-10-03T00%3A00%3A00Z",
		"result=unexpected",
		"limit=0",
		"limit=201",
		"offset=-1",
	} {
		bad := httptest.NewRequest(http.MethodGet, "/api/admin/audit?"+invalidQuery, nil)
		bad.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, bad)
		if response.Code != http.StatusBadRequest || len(store.filters) != 1 {
			t.Fatalf("invalid audit query %q returned %d and filters=%d", invalidQuery, response.Code, len(store.filters))
		}
	}
}
