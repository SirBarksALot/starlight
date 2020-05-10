package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang/gddo/httputil/header"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const concurrent = 10

type SafeMap struct {
	v   map[string]string
	mux sync.Mutex
}

func (results *SafeMap) Ins(key string, value string) {
	results.mux.Lock()
	// Lock so only one goroutine at a time can access the map results.v.
	results.v[key] = value
	results.mux.Unlock()
}

func (results *SafeMap) Retrieve(key string) string {
	results.mux.Lock()
	// Lock so only one goroutine at a time can access the map results.v.
	defer results.mux.Unlock()
	return results.v[key]
}

func checkDomain(domain string) string {
	var host = domain + ":443"
	conn, err := tls.Dial("tcp", host, nil)
	if err != nil {
		log.Print(err)
		return err.Error()
	}
	defer conn.Close()

	cert := conn.ConnectionState().PeerCertificates[0]
	timeNow := time.Now()
	expiresIn := strconv.FormatFloat(cert.NotAfter.Sub(timeNow).Hours()/24, 'f', 0, 64)

	return expiresIn
}

func processBatch(domains []string, output map[string]string) {
	var wg sync.WaitGroup
	var results = SafeMap{v: output}

	fmt.Println(domains)
	for _, domain := range domains {
		// Increment the WaitGroup counter.
		wg.Add(1)
		// Launch a goroutine to check the domain.
		go func(domain string) {
			results.Ins(domain, checkDomain(domain))
			// Decrement the counter when the goroutine completes.
			defer wg.Done()
		}(domain)
	}
	// Wait for all domains from batch to complete.
	wg.Wait()
	return
}

func checker(domainsList []string) map[string]string {
	output := make(map[string]string)

	for i := 0; i < len(domainsList); i += concurrent {
		if i+concurrent > len(domainsList) {
			processBatch(domainsList[i:], output)
		} else {
			processBatch(domainsList[i:i+concurrent], output)
		}
	}
	return output
}

type DomainList struct {
	Domains []string
}

type respData struct {
	Data        map[string]string
	RequestTime string
}

func handleRequest(w http.ResponseWriter, req *http.Request) {
	start := time.Now()

	if req.Header.Get("Content-Type") != "" {
		value, _ := header.ParseValueAndParams(req.Header, "Content-Type")
		if value != "application/json" {
			msg := "Content-Type header is not application/json"
			http.Error(w, msg, http.StatusUnsupportedMediaType)
			return
		}
	}
	req.Body = http.MaxBytesReader(w, req.Body, 1048576)
	dec := json.NewDecoder(req.Body)
	dec.DisallowUnknownFields()

	var doms DomainList
	err := dec.Decode(&doms)
	if err != nil {
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError

		switch {
		case errors.As(err, &syntaxError):
			msg := fmt.Sprintf("Request body contains badly-formed JSON (at position %d)", syntaxError.Offset)
			http.Error(w, msg, http.StatusBadRequest)

		case errors.Is(err, io.ErrUnexpectedEOF):
			msg := fmt.Sprintf("Request body contains badly-formed JSON")
			http.Error(w, msg, http.StatusBadRequest)

		case errors.As(err, &unmarshalTypeError):
			msg := fmt.Sprintf("Request body contains an invalid value for the %q field (at position %d)", unmarshalTypeError.Field, unmarshalTypeError.Offset)
			http.Error(w, msg, http.StatusBadRequest)

		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			msg := fmt.Sprintf("Request body contains unknown field %s", fieldName)
			http.Error(w, msg, http.StatusBadRequest)

		case errors.Is(err, io.EOF):
			msg := "Request body must not be empty"
			http.Error(w, msg, http.StatusBadRequest)

		case err.Error() == "http: request body too large":
			msg := "Request body must not be larger than 1MB"
			http.Error(w, msg, http.StatusRequestEntityTooLarge)

		default:
			log.Println(err.Error())
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
		return
	}

	var data = respData{Data: make(map[string]string)}

	data.Data = checker(doms.Domains)
	data.RequestTime = time.Since(start).String()

	js, err := json.Marshal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
	log.Printf("Request took %sto process", time.Since(start))
}

func main() {
	http.HandleFunc("/api/checker", handleRequest)
	log.Fatal(http.ListenAndServe(":8000", nil))
}
