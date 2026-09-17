package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"
)

func home(w http.ResponseWriter, req *http.Request) {
	fmt.Fprint(w, newTemplate())
}

func stat(w http.ResponseWriter, req *http.Request) {
	p := cleanSlash(req.FormValue("path"))
	info, err := os.Stat(store.workingPath + p)

	var resp *File
	if err != nil {
		resp = &File{
			Name: markdownName(p),
			Path: p,
		}
	} else {
		resp = &File{
			Name:      markdownName(p),
			Path:      p,
			Size:      info.Size(),
			Exists:    true,
			UpdatedAt: info.ModTime().UnixNano(),
		}
	}

	fmt.Fprint(w, jsonEncode(resp))
}

func file(w http.ResponseWriter, req *http.Request) {
	html, err := markdown(store.workingPath + cleanSlash(req.FormValue("path")))
	if err != nil {
		return
	}

	fmt.Fprint(w, html)
}

func files(w http.ResponseWriter, req *http.Request) {
	fmt.Fprint(w, jsonEncode(getFiles(store.workingPath)))
}

func forward(w http.ResponseWriter, req *http.Request) {
	p := store.workingPath + cleanSlash(req.URL.Query().Get("path"))

	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if data, err := ioutil.ReadFile(p); err == nil {
		w.Header().Set("Content-Type", contentType(p))
		w.Write(data)
		return
	}

	w.WriteHeader(http.StatusInternalServerError)
}

func main() {
	auth, err := openAuthFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer auth.db.Close()
	if len(os.Args) == 3 && os.Args[1] == "--set-password" {
		if err := auth.resetPasswordFromStdin(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		log.Print("Password updated; existing sessions revoked")
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--create-user" {
		if err := auth.createUserFromStdin(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		log.Print("User created")
		return
	}
	store.workingPath = getArgument()
	if err := validateDocumentRoot(store.workingPath, auth.dbPath); err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: ":3000", Handler: auth.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
