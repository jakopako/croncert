package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// The APIWriter is meant to write to a custom API and assumes many things.
// So currently, it is better not to use this APIWriter.
type APIWriter struct {
	writerConfig *WriterConfig
}

// NewAPIWriter returns a new APIWriter
func NewAPIWriter(wc *WriterConfig) *APIWriter {
	return &APIWriter{
		writerConfig: wc,
	}
}

func (f *APIWriter) Write(items chan map[string]interface{}) {
	client := &http.Client{
		Timeout: time.Second * 10,
	}
	apiURL := f.writerConfig.Uri
	apiUser := f.writerConfig.User
	apiPassword := f.writerConfig.Password

	deletedSources := map[string]bool{}
	nrItems := 0
	batch := []map[string]interface{}{}

	// This code assumes that within one source, items are ordered
	// by date ascending.
	for item := range items {
		nrItems++
		currentSrc := item["sourceUrl"].(string)
		if _, found := deletedSources[currentSrc]; !found {
			deletedSources[currentSrc] = true
			// delete all items from the given source
			firstDate, ok := item["date"].(time.Time)
			if !ok {
				log.Fatalf("error while trying to cast the date field of item %v to time.Time", item)
			}
			firstDateUTCF := firstDate.UTC().Format("2006-01-02 15:04")
			deleteURL := fmt.Sprintf("%s?sourceUrl=%s&datetime=%s", apiURL, url.QueryEscape(currentSrc), url.QueryEscape(firstDateUTCF))
			req, _ := http.NewRequest("DELETE", deleteURL, nil)
			req.SetBasicAuth(apiUser, apiPassword)
			resp, err := client.Do(req)
			if err != nil {
				log.Fatal(err)
			}
			if resp.StatusCode != 200 {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					log.Fatal(err)
				}
				log.Fatalf("something went wrong while deleting items. Status Code: %d\nUrl: %s Response: %s", resp.StatusCode, deleteURL, body)
			}
			resp.Body.Close()
		}
		batch = append(batch, item)
		if len(batch) == 100 {
			postBatch(client, batch, apiURL, apiUser, apiPassword)
			batch = []map[string]interface{}{}
		}
	}
	postBatch(client, batch, apiURL, apiUser, apiPassword)

	log.Printf("wrote %d items from %d sources to the api", nrItems, len(deletedSources))
}

func postBatch(client *http.Client, batch []map[string]interface{}, apiURL, apiUser, apiPassword string) {
	concertJSON, err := json.Marshal(batch)
	if err != nil {
		log.Fatal(err)
	}
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(concertJSON))
	req.Header = map[string][]string{
		"Content-Type": {"application/json"},
	}
	req.SetBasicAuth(apiUser, apiPassword)
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	if resp.StatusCode != 201 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		log.Fatalf("something went wrong while adding new events. Status Code: %d Response: %s", resp.StatusCode, body)
	}
	resp.Body.Close()

}
