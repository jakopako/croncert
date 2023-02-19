package scraper

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/goodsign/monday"
	"github.com/ilyakaznacheev/cleanenv"
	"github.com/jakopako/goskyr/fetch"
	"github.com/jakopako/goskyr/output"
	"github.com/jakopako/goskyr/utils"
	"golang.org/x/net/html"
	"gopkg.in/yaml.v2"
)

// GlobalConfig is used for storing global configuration parameters that
// are needed across all scrapers
type GlobalConfig struct {
	UserAgent string `yaml:"user-agent"`
}

// Config defines the overall structure of the scraper configuration.
// Values will be taken from a config yml file or environment variables
// or both.
type Config struct {
	Writer   output.WriterConfig `yaml:"writer,omitempty"`
	Scrapers []Scraper           `yaml:"scrapers,omitempty"`
	Global   GlobalConfig        `yaml:"global,omitempty"`
}

func NewConfig(configPath string) (*Config, error) {
	var config Config

	err := cleanenv.ReadConfig(configPath, &config)
	if err != nil {
		log.Fatal(err)
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	d := yaml.NewDecoder(file)
	if err := d.Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// RegexConfig is used for extracting a substring from a string based on the
// given Exp and Index
type RegexConfig struct {
	Exp   string `yaml:"exp"`
	Index int    `yaml:"index"`
}

// ElementLocation is used to find a specific string in a html document
type ElementLocation struct {
	Selector      string      `yaml:"selector,omitempty"`
	NodeIndex     int         `yaml:"node_index,omitempty"`
	ChildIndex    int         `yaml:"child_index,omitempty"`
	RegexExtract  RegexConfig `yaml:"regex_extract,omitempty"`
	Attr          string      `yaml:"attr,omitempty"`
	MaxLength     int         `yaml:"max_length,omitempty"`
	EntireSubtree bool        `yaml:"entire_subtree,omitempty"`
}

// CoveredDateParts is used to determine what parts of a date a
// DateComponent covers
type CoveredDateParts struct {
	Day   bool `yaml:"day"`
	Month bool `yaml:"month"`
	Year  bool `yaml:"year"`
	Time  bool `yaml:"time"`
}

// A DateComponent is used to find a specific part of a date within
// a html document
type DateComponent struct {
	Covers          CoveredDateParts `yaml:"covers"`
	ElementLocation ElementLocation  `yaml:"location"`
	Layout          []string         `yaml:"layout"`
}

// A Field contains all the information necessary to scrape
// a dynamic field from a website, ie a field who's value changes
// for each item
type Field struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value,omitempty"`
	Type  string `yaml:"type,omitempty"` // can currently be text, url or date
	// If a field can be found on a subpage the following variable has to contain a field name of
	// a field of type 'url' that is located on the main page.
	ElementLocation ElementLocation `yaml:"location,omitempty"`
	OnSubpage       string          `yaml:"on_subpage,omitempty"`    // applies to text, url, date
	CanBeEmpty      bool            `yaml:"can_be_empty,omitempty"`  // applies to text, url
	Components      []DateComponent `yaml:"components,omitempty"`    // applies to date
	DateLocation    string          `yaml:"date_location,omitempty"` // applies to date
	DateLanguage    string          `yaml:"date_language,omitempty"` // applies to date
	Hide            bool            `yaml:"hide,omitempty"`          // appliess to text, url, date
}

// A Filter is used to filter certain items from the result list
type Filter struct {
	Field string `yaml:"field"`
	Regex string `yaml:"regex"`
	Match bool   `yaml:"match"`
}

// A Scraper contains all the necessary config parameters and structs needed
// to extract the desired information from a website
type Scraper struct {
	Name                string   `yaml:"name"`
	URL                 string   `yaml:"url"`
	Item                string   `yaml:"item"`
	ExcludeWithSelector []string `yaml:"exclude_with_selector,omitempty"`
	Fields              []Field  `yaml:"fields,omitempty"`
	Filters             []Filter `yaml:"filters,omitempty"`
	Paginator           struct {
		Location ElementLocation `yaml:"location,omitempty"`
		MaxPages int             `yaml:"max_pages,omitempty"`
	} `yaml:"paginator,omitempty"`
	RenderJs bool `yaml:"renderJs,omitempty"`
}

// GetItems fetches and returns all items from a website according to the
// Scraper's paramaters
func (c Scraper) GetItems(globalConfig *GlobalConfig) ([]map[string]interface{}, error) {

	var items []map[string]interface{}

	pageURL := c.URL
	hasNextPage := true
	currentPage := 0
	var fetcher fetch.Fetcher
	if c.RenderJs {
		fetcher = &fetch.DynamicFetcher{
			UserAgent: globalConfig.UserAgent,
		}
	} else {
		fetcher = &fetch.StaticFetcher{
			UserAgent: globalConfig.UserAgent,
		}
	}
	for hasNextPage {
		res, err := fetcher.Fetch(pageURL)
		if err != nil {
			return items, err
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(res))
		if err != nil {
			return items, err
		}

		doc.Find(c.Item).Each(func(i int, s *goquery.Selection) {
			for _, excludeSelector := range c.ExcludeWithSelector {
				if s.Find(excludeSelector).Length() > 0 || s.Is(excludeSelector) {
					return
				}
			}

			currentItem := make(map[string]interface{})
			for _, f := range c.Fields {
				if f.Value != "" {
					// add static fields
					currentItem[f.Name] = f.Value
				} else {
					// handle all dynamic fields on the main page
					if f.OnSubpage == "" {
						err := extractField(&f, currentItem, s, pageURL)
						if err != nil {
							log.Printf("%s ERROR: error while parsing field %s: %v. Skipping item %v.", c.Name, f.Name, err, currentItem)
							return
						}
					}
				}
			}

			// handle all fields on subpages
			subDocs := make(map[string]*goquery.Document)
			for _, f := range c.Fields {
				if f.OnSubpage != "" && f.Value == "" {
					// check whether we fetched the page already
					subpageURL := fmt.Sprint(currentItem[f.OnSubpage])
					_, found := subDocs[subpageURL]
					if !found {
						subRes, err := fetcher.Fetch(subpageURL)
						if err != nil {
							log.Printf("%s ERROR: %v. Skipping item %v.", c.Name, err, currentItem)
							return
						}
						subDoc, err := goquery.NewDocumentFromReader(strings.NewReader(subRes))
						if err != nil {
							log.Printf("%s ERROR: error while reading document: %v. Skipping item %v", c.Name, err, currentItem)
							return
						}
						subDocs[subpageURL] = subDoc
					}
					err = extractField(&f, currentItem, subDocs[subpageURL].Selection, c.URL)
					if err != nil {
						log.Printf("%s ERROR: error while parsing field %s: %v. Skipping item %v.", c.Name, f.Name, err, currentItem)
						return
					}
				}
			}

			// check if item should be filtered
			filter, err := c.filterItem(currentItem)
			if err != nil {
				log.Fatalf("%s ERROR: error while applying filter: %v.", c.Name, err)
			}
			if filter {
				currentItem = c.removeHiddenFields(currentItem)
				items = append(items, currentItem)
			}
		})

		hasNextPage = false
		pageURL = getURLString(&c.Paginator.Location, doc.Selection, pageURL)
		if pageURL != "" {
			currentPage++
			if currentPage < c.Paginator.MaxPages || c.Paginator.MaxPages == 0 {
				hasNextPage = true
			}
		}
	}
	// TODO: check if the dates make sense. Sometimes we have to guess the year since it
	// does not appear on the website. In that case, eg. having a list of events around
	// the end of one year and the beginning of the next year we might want to change the
	// year of some events because our previous guess was rather naiv. We also might want
	// to make this functionality optional. See issue #68

	return items, nil
}

func (c *Scraper) filterItem(item map[string]interface{}) (bool, error) {
	nrMatchTrue := 0
	filterMatchTrue := false
	filterMatchFalse := true
	for _, filter := range c.Filters {
		regex, err := regexp.Compile(filter.Regex)
		if err != nil {
			return false, err
		}
		if fieldValue, found := item[filter.Field]; found {
			if filter.Match {
				nrMatchTrue++
				if regex.MatchString(fmt.Sprint(fieldValue)) {
					filterMatchTrue = true
				}
			} else {
				if regex.MatchString(fmt.Sprint(fieldValue)) {
					filterMatchFalse = false
				}
			}
		}
	}
	if nrMatchTrue == 0 {
		filterMatchTrue = true
	}
	return filterMatchTrue && filterMatchFalse, nil
}

func (c *Scraper) removeHiddenFields(item map[string]interface{}) map[string]interface{} {
	for _, f := range c.Fields {
		if f.Hide {
			delete(item, f.Name)
		}
	}
	return item
}

func extractField(field *Field, event map[string]interface{}, s *goquery.Selection, baseURL string) error {
	switch field.Type {
	case "text", "": // the default, ie when type is not configured, is 'text'
		ts, err := getTextString(&field.ElementLocation, s)
		if err != nil {
			return err
		}
		if !field.CanBeEmpty && ts == "" {
			return fmt.Errorf("field %s cannot be empty", field.Name)
		}
		event[field.Name] = ts
	case "url":
		url := getURLString(&field.ElementLocation, s, baseURL)
		if url == "" {
			url = baseURL
		}
		event[field.Name] = url
	case "date":
		d, err := getDate(field, s)
		if err != nil {
			return err
		}
		event[field.Name] = d
	default:
		return fmt.Errorf("field type '%s' does not exist", field.Type)
	}
	return nil
}

type datePart struct {
	stringPart  string
	layoutParts []string
}

func getDate(f *Field, s *goquery.Selection) (time.Time, error) {
	// time zone
	var t time.Time
	loc, err := time.LoadLocation(f.DateLocation)
	if err != nil {
		return t, err
	}

	// locale (language)
	mLocale := "de_DE"
	if f.DateLanguage != "" {
		mLocale = f.DateLanguage
	}

	// collect all the date parts
	dateParts := []datePart{}
	combinedParts := CoveredDateParts{}
	for _, c := range f.Components {
		if !hasAllDateParts(combinedParts) {
			if err := checkForDoubleDateParts(c.Covers, combinedParts); err != nil {
				return t, err
			}
			sp, err := getTextString(&c.ElementLocation, s)
			if err != nil {
				return t, err
			}
			if sp != "" {
				var lp []string
				for _, l := range c.Layout {
					lp = append(lp, strings.Replace(l, "p.m.", "pm", 1))
				}
				dateParts = append(dateParts, datePart{
					stringPart:  strings.Replace(sp, "p.m.", "pm", 1),
					layoutParts: lp,
				})
				combinedParts = mergeDateParts(combinedParts, c.Covers)
			}
		}
	}
	// adding default values where necessary
	if !combinedParts.Year {
		currentYear := time.Now().Year()
		dateParts = append(dateParts, datePart{
			stringPart:  strconv.Itoa(currentYear),
			layoutParts: []string{"2006"},
		})
	}
	if !combinedParts.Time {
		dateParts = append(dateParts, datePart{
			stringPart:  "20:00",
			layoutParts: []string{"15:04"},
		})
	}
	// currently not all date parts have default values
	if !combinedParts.Day || !combinedParts.Month {
		return t, errors.New("date parsing error: to generate a date at least a day and a month is needed")
	}

	var dateTimeString string
	dateTimeLayouts := []string{""}
	for _, dp := range dateParts {
		tmpDateTimeLayouts := dateTimeLayouts
		dateTimeLayouts = []string{}
		for _, tlp := range tmpDateTimeLayouts {
			for _, lp := range dp.layoutParts {
				dateTimeLayouts = append(dateTimeLayouts, tlp+lp+" ")
			}
		}
		dateTimeString += dp.stringPart + " "
	}
	dateTimeString = strings.Replace(dateTimeString, "Mrz", "Mär", 1) // hack for issue #47
	for _, dateTimeLayout := range dateTimeLayouts {
		t, err = monday.ParseInLocation(dateTimeLayout, dateTimeString, loc, monday.Locale(mLocale))
		if err == nil {
			return t, nil
		}
	}
	return t, err
}

func checkForDoubleDateParts(dpOne CoveredDateParts, dpTwo CoveredDateParts) error {
	if dpOne.Day && dpTwo.Day {
		return errors.New("date parsing error: 'day' covered at least twice")
	}
	if dpOne.Month && dpTwo.Month {
		return errors.New("date parsing error: 'month' covered at least twice")
	}
	if dpOne.Year && dpTwo.Year {
		return errors.New("date parsing error: 'year' covered at least twice")
	}
	if dpOne.Time && dpTwo.Time {
		return errors.New("date parsing error: 'time' covered at least twice")
	}
	return nil
}

func mergeDateParts(dpOne CoveredDateParts, dpTwo CoveredDateParts) CoveredDateParts {
	return CoveredDateParts{
		Day:   dpOne.Day || dpTwo.Day,
		Month: dpOne.Month || dpTwo.Month,
		Year:  dpOne.Year || dpTwo.Year,
		Time:  dpOne.Time || dpTwo.Time,
	}
}

func hasAllDateParts(cdp CoveredDateParts) bool {
	return cdp.Day && cdp.Month && cdp.Year && cdp.Time
}

func getURLString(e *ElementLocation, s *goquery.Selection, baseURL string) string {
	var urlVal, urlRes string
	u, _ := url.Parse(baseURL)
	if e.Attr == "" {
		// set attr to the default if not set
		e.Attr = "href"
	}
	if e.Selector == "" {
		urlVal = s.AttrOr(e.Attr, "")
	} else {
		fieldSelection := s.Find(e.Selector)
		if len(fieldSelection.Nodes) > e.NodeIndex {
			fieldNode := fieldSelection.Get(e.NodeIndex)
			for _, a := range fieldNode.Attr {
				if a.Key == e.Attr {
					urlVal = a.Val
					break
				}
			}
		}
	}

	if urlVal == "" {
		return ""
	} else if strings.HasPrefix(urlVal, "http") {
		urlRes = urlVal
	} else if strings.HasPrefix(urlVal, "?") {
		urlRes = fmt.Sprintf("%s://%s%s%s", u.Scheme, u.Host, u.Path, urlVal)
	} else {
		baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
		if !strings.HasPrefix(urlVal, "/") {
			baseURL = baseURL + "/"
		}
		urlRes = fmt.Sprintf("%s%s", baseURL, urlVal)
	}

	urlRes = strings.TrimSpace(urlRes)
	return urlRes
}

func getTextString(t *ElementLocation, s *goquery.Selection) (string, error) {
	var fieldString string
	var err error
	fieldSelection := s.Find(t.Selector)
	if len(fieldSelection.Nodes) > t.NodeIndex {
		if t.Attr == "" {
			if t.EntireSubtree {
				// copied from https://github.com/PuerkitoBio/goquery/blob/v1.8.0/property.go#L62
				var buf bytes.Buffer
				var f func(*html.Node)
				f = func(n *html.Node) {
					if n.Type == html.TextNode {
						// Keep newlines and spaces, like jQuery
						buf.WriteString(n.Data)
					}
					if n.FirstChild != nil {
						for c := n.FirstChild; c != nil; c = c.NextSibling {
							f(c)
						}
					}
				}
				f(fieldSelection.Get(t.NodeIndex))
				fieldString = buf.String()
			} else {
				fieldNode := fieldSelection.Get(t.NodeIndex).FirstChild
				currentChildIndex := 0
				for fieldNode != nil {
					// for the case where we want to find the correct string
					// by regex (checking all the children and taking the first one that matches the regex)
					// the ChildIndex has to be set to -1 to
					// distinguish from the default case 0. So when we explicitly set ChildIndex to -1 it means
					// check _all_ of the children.
					if currentChildIndex == t.ChildIndex || t.ChildIndex == -1 {
						if fieldNode.Type == html.TextNode {
							fieldString, err = extractStringRegex(&t.RegexExtract, fieldNode.Data)
							if err == nil {
								fieldString = strings.TrimSpace(fieldString)
								if t.MaxLength > 0 {
									fieldString = utils.ShortenString(fieldString, t.MaxLength)
								}
								return fieldString, nil
							} else if t.ChildIndex != -1 {
								// only in case we do not (ab)use the regex to search across all children
								// we want to return the err. Also, we still return the fieldString as
								// this might be useful for narrowing down the reason for the error.
								return fieldString, err
							}
						}
					}
					fieldNode = fieldNode.NextSibling
					currentChildIndex++
				}
			}
		} else {
			// WRONG
			// It could be the case that there are multiple nodes that match the selector
			// and we don't want the attr of the first node...
			fieldString = fieldSelection.AttrOr(t.Attr, "")
		}
	}
	// automatically trimming whitespaces might be confusing in some cases...
	fieldString = strings.TrimSpace(fieldString)
	fieldString, err = extractStringRegex(&t.RegexExtract, fieldString)
	if err != nil {
		return fieldString, err
	}
	if t.MaxLength > 0 && t.MaxLength < len(fieldString) {
		fieldString = fieldString[:t.MaxLength] + "..."
	}
	return fieldString, nil
}

func extractStringRegex(rc *RegexConfig, s string) (string, error) {
	extractedString := s
	if rc.Exp != "" {
		regex, err := regexp.Compile(rc.Exp)
		if err != nil {
			return "", err
		}
		matchingStrings := regex.FindAllString(s, -1)
		if len(matchingStrings) == 0 {
			msg := fmt.Sprintf("no matching strings found for regex: %s", rc.Exp)
			return "", errors.New(msg)
		}
		if rc.Index == -1 {
			extractedString = matchingStrings[len(matchingStrings)-1]
		} else {
			if rc.Index >= len(matchingStrings) {
				msg := fmt.Sprintf("regex index out of bounds. regex '%s' gave only %d matches", rc.Exp, len(matchingStrings))
				return "", errors.New(msg)
			}
			extractedString = matchingStrings[rc.Index]
		}
	}
	return extractedString, nil
}
