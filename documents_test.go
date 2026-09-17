package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDocumentVersions(t *testing.T) {
	root := t.TempDir()
	docs, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer docs.Close()
	history, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	name := "技术方案.md"
	first, err := replaceDocument(docs, history, name, "tester", []byte("# One"))
	if err != nil || !first["changed"].(bool) {
		t.Fatal(first, err)
	}
	second, err := replaceDocument(docs, history, name, "tester", []byte("# Two"))
	if err != nil {
		t.Fatal(err)
	}
	id := second["previous_version"].(string)
	v, err := loadDocumentVersion(history, name, id)
	if err != nil || string(v.Content) != "# One" {
		t.Fatal(v, err)
	}
	same, err := replaceDocument(docs, history, name, "tester", []byte("# Two"))
	if err != nil || same["changed"].(bool) {
		t.Fatal(same, err)
	}
	restored, err := replaceDocument(docs, history, name, "tester", v.Content)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := loadDocumentVersion(history, name, restored["previous_version"].(string))
	if err != nil || string(previous.Content) != "# Two" {
		t.Fatal(previous, err)
	}
	if data, _ := docs.ReadFile(name); string(data) != "# One" {
		t.Fatal("restore failed")
	}
	// A failed backup must never replace the current file.
	history.Close()
	if _, err = replaceDocument(docs, history, name, "tester", []byte("lost")); err == nil {
		t.Fatal("expected backup failure")
	}
	if data, _ := docs.ReadFile(name); string(data) != "# One" {
		t.Fatal("lost original after backup error")
	}
}

func TestDocumentPathAndIntegrity(t *testing.T) {
	for _, name := range []string{"../x.md", "/x.md", "a/../x.md", "x.txt", ".hidden.md", "a\\b.md", "a:b.md", "a\x00.md", "a\n.md"} {
		if documentName(name) == nil {
			t.Fatal(name)
		}
	}
	docs, _ := os.OpenRoot(t.TempDir())
	defer docs.Close()
	history, _ := os.OpenRoot(t.TempDir())
	defer history.Close()
	docs.WriteFile("Case.md", []byte("original"), 0600)
	if _, err := replaceDocument(docs, history, "case.md", "test", []byte("alias")); err == nil {
		t.Fatal("case alias accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	os.WriteFile(outside, []byte("outside"), 0600)
	if err := docs.Symlink(outside, "link.md"); err == nil {
		if _, err = replaceDocument(docs, history, "link.md", "test", []byte("bad")); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	id, err := archiveDocument(history, "Case.md", "test", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	history.WriteFile(digestDocument([]byte("Case.md"))+"/"+id+".json", []byte(`{"id":"wrong"}`), 0600)
	if _, err = loadDocumentVersion(history, "Case.md", id); err == nil {
		t.Fatal("corrupt history accepted")
	}
	if _, err = loadDocumentVersion(history, "Case.md", "../outside"); err == nil {
		t.Fatal("version traversal accepted")
	}
}

func TestDocumentHTTPAndConcurrency(t *testing.T) {
	a, err := openAuth(filepath.Join(t.TempDir(), "auth.db"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	if err = a.createUser("reader", "test-only-password-42"); err != nil {
		t.Fatal(err)
	}
	oldRoot := store.workingPath
	store.workingPath = t.TempDir()
	defer func() { store.workingPath = oldRoot }()
	cookie := signedIn(t, a)
	endpoint := "/api/documents/sync?path=" + url.QueryEscape("并发.md")
	if w := call(a, "PUT", endpoint, "# anon", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(a, "GET", endpoint, "", cookie); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := call(a, "PUT", endpoint, strings.Repeat("x", documentLimit+1), cookie); w.Code != 413 {
		t.Fatal(w.Code)
	}
	if w := call(a, "PUT", endpoint, "\xff", cookie); w.Code != 400 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("PUT", endpoint, strings.NewReader("# cross"))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	const count = 6
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := call(a, "PUT", endpoint, fmt.Sprintf("# version %d", i), cookie)
			if w.Code != 200 {
				t.Errorf("sync: %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	w = call(a, "GET", "/api/documents/history?path="+url.QueryEscape("并发.md"), "", cookie)
	var response struct {
		Versions []documentVersion `json:"versions"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Versions) != count-1 {
		t.Fatal(w.Body.String(), err)
	}
	seen := map[string]bool{}
	current, _ := os.ReadFile(filepath.Join(store.workingPath, "并发.md"))
	seen[string(current)] = true
	for _, v := range response.Versions {
		w = call(a, "GET", "/api/documents/version?path="+url.QueryEscape("并发.md")+"&version="+v.ID, "", cookie)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		seen[w.Body.String()] = true
	}
	if len(seen) != count {
		t.Fatal("a concurrent version was lost", seen)
	}
	v := response.Versions[0]
	w = call(a, "POST", "/api/documents/restore?path="+url.QueryEscape("并发.md")+"&version="+v.ID, "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call(a, "GET", "/documents", "", nil); w.Code != 303 {
		t.Fatal(w.Code)
	}
}
