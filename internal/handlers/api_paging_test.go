package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/authz"
)

// The catalogue grew a page boundary and a batch read. These cases pin the two
// properties a caller depends on: a page is a page of what the caller may see
// (never a short window caused by filtering afterwards), and the counts describe
// the catalogue rather than the window, or every tab but the selected one would
// read zero.

func seedSkills(t *testing.T, srv *httptest.Server, n int) []string {
	t.Helper()
	names := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	ids := make([]string, 0, n)
	for _, name := range names[:n] {
		res, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources",
			`{"kind":"skill","name":"`+name+`","version":"1.0.0"}`)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("seed %s: %d %v", name, res.StatusCode, body)
		}
		id, _ := body["id"].(string)
		if id == "" {
			t.Fatalf("seed %s returned no id: %v", name, body)
		}
		ids = append(ids, id)
	}
	return ids
}

func readPage(t *testing.T, body map[string]any) (ids []string, total, limit, offset int) {
	t.Helper()
	list, _ := body["resources"].([]any)
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	total = int(body["total"].(float64))
	limit = int(body["limit"].(float64))
	offset = int(body["offset"].(float64))
	return ids, total, limit, offset
}

func TestListingPagesThroughTheCatalogue(t *testing.T) {
	srv := governanceServer(t)
	all := seedSkills(t, srv, 5)

	// A page is bounded, and it says how many records exist in total.
	res, body := doJSON(t, http.MethodGet, srv.URL+"/api/resources?limit=2", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first page = %d %v", res.StatusCode, body)
	}
	first, total, limit, offset := readPage(t, body)
	if len(first) != 2 || total != 5 || limit != 2 || offset != 0 {
		t.Fatalf("page = %v of %d (limit %d, offset %d)", first, total, limit, offset)
	}

	// The next page continues instead of repeating: the listing is sorted by id,
	// so a stable order is what makes "next" meaningful.
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/resources?limit=2&offset=2", "")
	second, _, _, offset := readPage(t, body)
	if len(second) != 2 || offset != 2 {
		t.Fatalf("second page = %v (offset %d)", second, offset)
	}
	for _, id := range second {
		for _, prev := range first {
			if id == prev {
				t.Fatalf("page two repeats %s from page one", id)
			}
		}
	}

	// The last page is short, not padded, and the total does not shrink with it.
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/resources?limit=2&offset=4", "")
	last, total, _, _ := readPage(t, body)
	if len(last) != 1 || total != 5 {
		t.Fatalf("last page = %v of %d", last, total)
	}

	// An offset past the end is an empty page with an honest total, not an error:
	// the console's own counter is what tells the user to go back.
	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/resources?limit=2&offset=99", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("offset past the end = %d", res.StatusCode)
	}
	beyond, total, _, _ := readPage(t, body)
	if len(beyond) != 0 || total != 5 {
		t.Fatalf("past the end = %d records of %d", len(beyond), total)
	}
	// The list is an empty array on the wire, never null: the console maps it.
	if body["resources"] == nil {
		t.Fatal("an empty page must carry [], not null")
	}

	// No limit means everything, which is what every caller before paging asked
	// for: an existing client keeps working unchanged.
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/resources", "")
	unpaged, total, limit, _ := readPage(t, body)
	if len(unpaged) != len(all) || total != len(all) || limit != 0 {
		t.Fatalf("unpaged = %d of %d (limit %d)", len(unpaged), total, limit)
	}
}

// TestListingCountsDescribeTheCatalogueNotThePage: the tab strip reads counts, so
// one kind's page must not zero every other tab.
func TestListingCountsDescribeTheCatalogueNotThePage(t *testing.T) {
	srv := governanceServer(t)
	seedSkills(t, srv, 3)
	if res, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources",
		`{"kind":"model","name":"gpt-demo","version":"1.0.0"}`); res.StatusCode != http.StatusCreated {
		t.Fatalf("seed model: %d %v", res.StatusCode, body)
	}

	_, body := doJSON(t, http.MethodGet, srv.URL+"/api/resources?kind=skill&limit=1", "")
	counts, _ := body["counts"].(map[string]any)
	if got := int(counts["skill"].(float64)); got != 3 {
		t.Fatalf("skill count = %d, want 3", got)
	}
	if got := int(counts["model"].(float64)); got != 1 {
		t.Fatalf("model count = %d, want 1 even though the page is filtered to skills", got)
	}
	// The page itself is still filtered, and still one record long.
	ids, total, _, _ := readPage(t, body)
	if len(ids) != 1 || total != 3 {
		t.Fatalf("page = %d of %d", len(ids), total)
	}
}

func TestListingRefusesAnImpossiblePage(t *testing.T) {
	srv := governanceServer(t)
	for _, q := range []string{"limit=abc", "limit=-1", "limit=201", "offset=-1", "offset=half"} {
		res, body := doJSON(t, http.MethodGet, srv.URL+"/api/resources?"+q, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("?%s = %d, want 400 (%v)", q, res.StatusCode, body)
		}
		if code, _ := body["code"].(string); code != "bad_request" {
			t.Fatalf("?%s code = %q, want bad_request", q, code)
		}
	}
}

// TestBatchGetResourcesAnswersInRequestOrder is the point of the endpoint: the
// caller zips the answer back onto what it asked for, so order is part of the
// contract and a missing id is reported rather than dropped.
func TestBatchGetResourcesAnswersInRequestOrder(t *testing.T) {
	srv := governanceServer(t)
	ids := seedSkills(t, srv, 2)

	res, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources/batch",
		`{"ids":["`+ids[1]+`","gone","`+ids[0]+`","`+ids[1]+`"]}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("batch = %d %v", res.StatusCode, body)
	}
	list, _ := body["resources"].([]any)
	if len(list) != 2 {
		t.Fatalf("resources = %d, want 2 (the repeated id is deduplicated)", len(list))
	}
	for i, want := range []string{ids[1], ids[0]} {
		entry, _ := list[i].(map[string]any)
		if entry["id"] != want {
			t.Fatalf("position %d = %v, want %s", i, entry["id"], want)
		}
	}
	missing, _ := body["missing"].([]any)
	if len(missing) != 1 || missing[0] != "gone" {
		t.Fatalf("missing = %v, want [gone]", missing)
	}
}

func TestBatchGetResourcesRefusesAnUnusableBatch(t *testing.T) {
	srv := governanceServer(t)
	tooMany := make([]string, 0, maxBatchIDs+1)
	for i := 0; i <= maxBatchIDs; i++ {
		tooMany = append(tooMany, fmt.Sprintf(`"id-%d"`, i))
	}
	bodies := []string{
		`{"ids":[]}`,      // nothing to read
		`{"ids":[""]}`,    // an id that cannot exist
		`{"ids":["   "]}`, // whitespace is not an id
		`{"ids":[` + strings.Join(tooMany, ",") + `]}`, // over the cap
		`{"ids":`, // malformed json
	}
	for _, body := range bodies {
		res, payload := doJSON(t, http.MethodPost, srv.URL+"/api/resources/batch", body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400 (%v)", body, res.StatusCode, payload)
		}
	}
	// Exactly the cap is a legitimate batch.
	atCap := make([]string, 0, maxBatchIDs)
	for i := 0; i < maxBatchIDs; i++ {
		atCap = append(atCap, fmt.Sprintf(`"id-%d"`, i))
	}
	if res, _ := doJSON(t, http.MethodPost, srv.URL+"/api/resources/batch",
		`{"ids":[`+strings.Join(atCap, ",")+`]}`); res.StatusCode != http.StatusOK {
		t.Fatalf("a batch of exactly %d ids = %d, want 200", maxBatchIDs, res.StatusCode)
	}
}

// TestBatchGetResourcesHidesWhatTheCallerCannotSee: an id the caller may not read
// is "missing", exactly like a GET of somebody else's record is a 404. The batch
// endpoint must not become an existence oracle.
func TestBatchGetResourcesHidesWhatTheCallerCannotSee(t *testing.T) {
	d, az := authTestDeps(t, "")
	h := Register(d)
	owner := bootstrapOwner(t, h)

	created := do(t, h, http.MethodPost, "/api/resources",
		`{"kind":"skill","name":"secret-sop","version":"1.0.0","visibility":"restricted"}`, authHeader(owner))
	if created.Code != http.StatusCreated {
		t.Fatalf("create private resource: %d %s", created.Code, created.Body.String())
	}
	var entry map[string]any
	decodeJSON(t, created, &entry)
	secretID, _ := entry["id"].(string)

	// A member who may read the catalogue but was granted nothing on that record.
	if w := do(t, h, http.MethodPost, "/api/users",
		`{"email":"member@example.com","password":"correct horse","name":"Member"}`, authHeader(owner)); w.Code != http.StatusCreated {
		t.Fatalf("create member: %d %s", w.Code, w.Body.String())
	}
	memberCookie := sessionOf(t, h, "member@example.com", "correct horse")
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(memberCookie)), &me)
	az.roles[me.User.ID] = []string{"role-viewer"}
	az.rolePerm["role-viewer"] = []string{authz.PermResourcesRead}

	type batchResult struct {
		Resources []map[string]any `json:"resources"`
		Missing   []string         `json:"missing"`
	}
	read := func(cookie string) batchResult {
		t.Helper()
		w := do(t, h, http.MethodPost, "/api/resources/batch", `{"ids":["`+secretID+`"]}`, authHeader(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("batch: %d %s", w.Code, w.Body.String())
		}
		var out batchResult
		decodeJSON(t, w, &out)
		return out
	}

	hidden := read(memberCookie)
	if len(hidden.Resources) != 0 {
		t.Fatalf("a private record must not be returned: %v", hidden.Resources)
	}
	if len(hidden.Missing) != 1 || hidden.Missing[0] != secretID {
		t.Fatalf("missing = %v, want the unreadable id", hidden.Missing)
	}

	// The owner reads the same id and gets the record.
	visible := read(owner)
	if len(visible.Resources) != 1 || visible.Resources[0]["id"] != secretID {
		t.Fatalf("owner batch = %v / %v", visible.Resources, visible.Missing)
	}
}
