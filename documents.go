package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const documentLimit = 8 << 20

var documentMu sync.Mutex // One writer process per document/history volume.
var versionPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[a-f0-9]{16}$`)

type documentVersion struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	SavedAt string `json:"saved_at"`
	SavedBy string `json:"saved_by"`
	SHA256  string `json:"sha256"`
	Size    int    `json:"size"`
	Content []byte `json:"content,omitempty"`
}

func documentName(name string) error {
	if !utf8.ValidString(name) || !fs.ValidPath(name) || len(name) > 512 || !strings.EqualFold(path.Ext(name), ".md") {
		return errors.New("需要有效的相对 .md 路径")
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.TrimSpace(part) != part || strings.ContainsAny(part, "\\:\x00<>\"|?*\r\n\t") || strings.HasSuffix(part, ".") {
			return errors.New("文件名包含不支持的字符")
		}
	}
	return nil
}

func digestDocument(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (a *authStore) documentRoots() (*os.Root, *os.Root, error) {
	docs, err := os.OpenRoot(store.workingPath)
	if err != nil {
		return nil, nil, err
	}
	historyPath := filepath.Join(filepath.Dir(a.dbPath), "document-history")
	if err = os.MkdirAll(historyPath, 0700); err != nil {
		docs.Close()
		return nil, nil, err
	}
	if err = validateDocumentRoot(store.workingPath, historyPath); err != nil {
		docs.Close()
		return nil, nil, err
	}
	history, err := os.OpenRoot(historyPath)
	if err != nil {
		docs.Close()
		return nil, nil, err
	}
	return docs, history, nil
}

// Reject links and case aliases, even on case-insensitive host bind mounts.
func checkDocumentPath(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parent := strings.Join(parts[:i], "/")
		if parent == "" {
			parent = "."
		}
		dir, err := root.Open(parent)
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), part) && entry.Name() != part {
				return errors.New("文件名大小写与现有文件不一致")
			}
		}
		p := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) && i == len(parts)-1 {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("不支持符号链接")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return errors.New("父路径不是目录")
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return errors.New("目标不是普通文件")
		}
	}
	return nil
}

func readDocument(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, documentLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > documentLimit {
		return nil, errors.New("文件超过 8 MiB")
	}
	return b, nil
}

func atomicDocument(root *os.Root, name string, data []byte) error {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), ".upload-"+hex.EncodeToString(random))
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func archiveDocument(history *os.Root, name, actor string, data []byte) (string, error) {
	key := digestDocument([]byte(name))
	if err := history.MkdirAll(key, 0700); err != nil {
		return "", err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	id := now.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random)
	v := documentVersion{ID: id, Path: name, SavedAt: now.Format(time.RFC3339Nano), SavedBy: actor, SHA256: digestDocument(data), Size: len(data), Content: data}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if err = atomicDocument(history, key+"/"+id+".json", b); err != nil {
		return "", err
	}
	return id, nil
}

func loadDocumentVersion(history *os.Root, name, id string) (documentVersion, error) {
	var v documentVersion
	if !versionPattern.MatchString(id) {
		return v, errors.New("无效版本号")
	}
	f, err := history.Open(digestDocument([]byte(name)) + "/" + id + ".json")
	if err != nil {
		return v, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, documentLimit*2)).Decode(&v)
	if err == nil && (v.ID != id || v.Path != name || v.Size != len(v.Content) || v.SHA256 != digestDocument(v.Content)) {
		err = errors.New("历史版本校验失败")
	}
	return v, err
}

func replaceDocument(docs, history *os.Root, name, actor string, data []byte) (map[string]interface{}, error) {
	if err := checkDocumentPath(docs, name); err != nil {
		return nil, err
	}
	old, err := readDocument(docs, name)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	result := map[string]interface{}{"path": name, "sha256": digestDocument(data), "changed": false, "previous_version": ""}
	if exists && bytes.Equal(old, data) {
		return result, nil
	}
	if exists {
		id, err := archiveDocument(history, name, actor, old)
		if err != nil {
			return nil, err
		}
		result["previous_version"] = id
	}
	if err = atomicDocument(docs, name, data); err != nil {
		return nil, err
	}
	result["changed"] = true
	return result, nil
}

func (a *authStore) documentAPI(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("path")
	if err := documentName(name); err != nil {
		fail(w, 400, err.Error())
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/documents/")
	want := "GET"
	if action == "sync" {
		want = "PUT"
	}
	if action == "restore" {
		want = "POST"
	}
	if action != "sync" && action != "history" && action != "version" && action != "restore" {
		fail(w, 404, "接口不存在")
		return
	}
	if r.Method != want {
		fail(w, 405, "请使用 "+want)
		return
	}
	var data []byte
	if action == "sync" {
		var err error
		data, err = io.ReadAll(http.MaxBytesReader(w, r.Body, documentLimit))
		if err != nil {
			fail(w, 413, "文件超过 8 MiB 或读取失败")
			return
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			fail(w, 400, "请上传 UTF-8 Markdown 文本")
			return
		}
	}
	documentMu.Lock()
	defer documentMu.Unlock()
	docs, history, err := a.documentRoots()
	if err != nil {
		fail(w, 503, "文档存储不可用")
		return
	}
	defer docs.Close()
	defer history.Close()
	if action == "sync" || action == "restore" {
		if action == "restore" {
			v, e := loadDocumentVersion(history, name, r.URL.Query().Get("version"))
			if e != nil {
				fail(w, 404, "版本不存在或校验失败")
				return
			}
			data = v.Content
		}
		actor, _, err := a.session(r)
		if err != nil {
			fail(w, 401, "登录已过期")
			return
		}
		result, err := replaceDocument(docs, history, name, actor, data)
		if err != nil {
			fail(w, 409, "同步失败，当前文件未替换；请检查路径、权限或磁盘空间")
			return
		}
		respond(w, 200, result)
		return
	}
	if action == "version" {
		v, err := loadDocumentVersion(history, name, r.URL.Query().Get("version"))
		if err != nil {
			fail(w, 404, "版本不存在或校验失败")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", `attachment; filename="version.md"`)
		}
		w.Write(v.Content)
		return
	}
	versions := []documentVersion{}
	dir, err := history.Open(digestDocument([]byte(name)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fail(w, 503, "无法读取历史记录")
		return
	}
	if err == nil {
		entries, e := dir.ReadDir(-1)
		dir.Close()
		if e != nil {
			fail(w, 503, "无法读取历史记录")
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			v, e := loadDocumentVersion(history, name, strings.TrimSuffix(entry.Name(), ".json"))
			if e != nil {
				fail(w, 503, "历史记录校验失败")
				return
			}
			v.Content = nil
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].ID > versions[j].ID })
	respond(w, 200, map[string]interface{}{"path": name, "versions": versions})
}

//go:embed ui/documents.html
var documentsPage string

func documentPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "请使用 GET")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, documentsPage)
}
