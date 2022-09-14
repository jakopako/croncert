package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

type StdoutWriter struct{}

func (s *StdoutWriter) Write(items chan map[string]interface{}, wg *sync.WaitGroup) {
	defer wg.Done()
	for item := range items {
		// We cannot use the following line of code because it automatically replaces certain html characters
		// with the corresponding Unicode replacement rune.
		// itemsJson, err := json.MarshalIndent(items, "", "  ")
		// if err != nil {
		// 	log.Print(err.Error())
		// }
		// See
		// https://stackoverflow.com/questions/28595664/how-to-stop-json-marshal-from-escaping-and
		// https://developpaper.com/the-solution-of-escaping-special-html-characters-in-golang-json-marshal/
		buffer := &bytes.Buffer{}
		encoder := json.NewEncoder(buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(item); err != nil {
			log.Printf("StdoutWriter ERROR while writing item %v: %v", item, err)
			continue
		}

		var indentBuffer bytes.Buffer
		if err := json.Indent(&indentBuffer, buffer.Bytes(), "", "  "); err != nil {
			log.Printf("StdoutWriter ERROR while writing item %v: %v", item, err)
			continue
		}
		fmt.Print(indentBuffer.String())
	}
}
