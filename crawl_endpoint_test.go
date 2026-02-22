package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
)

func init() {
	log.SetOutput(io.Discard)
}

type TestResponse struct {
	header     http.Header
	StatusCode int
	Content    []byte
}

func (response *TestResponse) Header() http.Header {
	return response.header
}

func (response *TestResponse) Write(content []byte) (int, error) {
	response.Content = content
	return len(content), nil
}

func (response *TestResponse) WriteHeader(statusCode int) {
	response.StatusCode = statusCode
}

func (response *TestResponse) DecodeJson() (map[string]any, error) {
	var data map[string]any
	err := json.Unmarshal(response.Content, &data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func callEndpoint(request *http.Request) *TestResponse {
	response := TestResponse{
		header: http.Header{},
	}
	CrawlEndpoint(&response, request)
	return &response
}

func expectErrorWithName(t *testing.T, response *TestResponse, expectedErrorName string) bool {
	jsonData, err := response.DecodeJson()
	if err != nil {
		t.Error(err)
		return false
	}

	errorName, exists := jsonData["error"]
	if !exists {
		t.Error("error property does not exist on response")
		return false
	}

	errorName, isString := errorName.(string)
	if !isString {
		t.Error("error property is not a string")
		return false
	}

	if errorName != expectedErrorName {
		t.Errorf("Incorrect error received: %s", errorName)
		return false
	}

	return true
}

func TestMethodNotAllowed(t *testing.T) {
	request, err := http.NewRequest("GET", "/crawl", nil)
	if err != nil {
		panic(err)
	}
	response := callEndpoint(request)

	if response.StatusCode != 405 {
		t.Errorf("Got status code %d", response.StatusCode)
		return
	}

	expectErrorWithName(t, response, "method not allowed")
}

func TestInvalidContentType(t *testing.T) {
	request, err := http.NewRequest("POST", "/crawl", strings.NewReader("{}"))
	if err != nil {
		panic(err)
	}
	request.Header.Add("Content-Type", "application/pdf")
	response := callEndpoint(request)

	if response.StatusCode != 400 {
		t.Errorf("Got status code %d", response.StatusCode)
		return
	}

	expectErrorWithName(t, response, "content type must be application/json")
}

func TestInvalidJson(t *testing.T) {
	request, err := http.NewRequest("POST", "/crawl", strings.NewReader("hello, world"))
	if err != nil {
		panic(err)
	}
	request.Header.Add("Content-Type", "application/json")
	response := callEndpoint(request)

	if response.StatusCode != 400 {
		t.Errorf("Got status code %d", response.StatusCode)
		return
	}

	if !expectErrorWithName(t, response, "invalid json") {
		return
	}

	// Send a string instead of an array of urls
	request, err = http.NewRequest("POST", "/crawl", strings.NewReader("{\"urls\": \"hello!\"}"))
	if err != nil {
		panic(err)
	}
	request.Header.Add("Content-Type", "application/json")
	response = callEndpoint(request)

	if response.StatusCode != 400 {
		t.Errorf("Got status code %d", response.StatusCode)
		return
	}

	expectErrorWithName(t, response, "invalid json")

	request, err = http.NewRequest("POST", "/crawl", strings.NewReader("{}"))
	if err != nil {
		panic(err)
	}
	request.Header.Add("Content-Type", "application/json")
	response = callEndpoint(request)

	if response.StatusCode != 400 {
		t.Errorf("Got status code %d", response.StatusCode)
		return
	}

	expectErrorWithName(t, response, "invalid json")
}

func TestDecodeResults(t *testing.T) {
	withResults := map[string]any{
		"results": []any{
			map[string]any{"url": "https://example.com"},
		},
	}
	results := decodeResults(withResults)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	withArray := []any{
		map[string]any{"url": "https://example.com"},
	}
	results = decodeResults(withArray)
	if len(results) != 1 {
		t.Fatalf("expected 1 result from top-level array, got %d", len(results))
	}
}

func TestExtractMarkdownPrefersFilteredMarkdown(t *testing.T) {
	result := map[string]any{
		"markdown": map[string]any{
			"raw_markdown": "raw markdown",
			"fit_markdown": "fit markdown",
		},
	}

	markdown := extractMarkdown(result)
	if markdown != "fit markdown" {
		t.Fatalf("unexpected markdown value: %q", markdown)
	}
}

func TestNormalizeRequestUrls(t *testing.T) {
	requestData := Request{
		Url: "https://example.com/single",
	}
	urls := normalizeRequestUrls(requestData)
	if len(urls) != 1 || urls[0] != "https://example.com/single" {
		t.Fatalf("unexpected normalized urls for single url: %#v", urls)
	}

	requestData = Request{
		Urls: []string{
			"https://example.com/a",
			"",
			"https://example.com/b",
		},
		Url: "https://example.com/c",
	}
	urls = normalizeRequestUrls(requestData)
	if len(urls) != 3 {
		t.Fatalf("expected 3 urls, got %#v", urls)
	}
}

func TestCrawlRequestPayloadCandidates(t *testing.T) {
	expectDefaultConfigs := func(payload map[string]any) {
		browserConfigValue, hasBrowserConfig := payload["browserConfig"]
		if !hasBrowserConfig {
			t.Fatalf("expected payload to include browserConfig, got %#v", payload)
		}

		browserConfig, isMap := browserConfigValue.(map[string]any)
		if !isMap {
			t.Fatalf("expected browserConfig to be an object, got %#v", browserConfigValue)
		}

		textMode, isBool := browserConfig["text_mode"].(bool)
		if !isBool || !textMode {
			t.Fatalf("expected browserConfig.text_mode=true, got %#v", browserConfig["text_mode"])
		}

		runConfigValue, hasRunConfig := payload["crawlerRunConfig"]
		if !hasRunConfig {
			t.Fatalf("expected payload to include crawlerRunConfig, got %#v", payload)
		}

		runConfig, isMap := runConfigValue.(map[string]any)
		if !isMap {
			t.Fatalf("expected crawlerRunConfig to be an object, got %#v", runConfigValue)
		}

		for _, key := range []string{"remove_overlay_elements", "magic", "exclude_all_images"} {
			value, isBool := runConfig[key].(bool)
			if !isBool || !value {
				t.Fatalf("expected crawlerRunConfig.%s=true, got %#v", key, runConfig[key])
			}
		}
	}

	singleCandidates := crawlRequestPayloadCandidates([]string{"https://example.com/single"})
	if len(singleCandidates) != 2 {
		t.Fatalf("expected 2 payload candidates for single url, got %d", len(singleCandidates))
	}

	var singleAsMap map[string]any
	if err := json.Unmarshal(singleCandidates[0], &singleAsMap); err != nil {
		t.Fatal(err)
	}
	if _, hasURL := singleAsMap["url"]; !hasURL {
		t.Fatalf("expected first single payload to include url field, got %#v", singleAsMap)
	}
	expectDefaultConfigs(singleAsMap)

	var singleAsUrlsMap map[string]any
	if err := json.Unmarshal(singleCandidates[1], &singleAsUrlsMap); err != nil {
		t.Fatal(err)
	}
	if _, hasUrls := singleAsUrlsMap["urls"]; !hasUrls {
		t.Fatalf("expected second single payload to include urls field, got %#v", singleAsUrlsMap)
	}
	expectDefaultConfigs(singleAsUrlsMap)

	multiCandidates := crawlRequestPayloadCandidates([]string{"https://example.com/a", "https://example.com/b"})
	if len(multiCandidates) != 1 {
		t.Fatalf("expected 1 payload candidate for multi url, got %d", len(multiCandidates))
	}

	var multiAsMap map[string]any
	if err := json.Unmarshal(multiCandidates[0], &multiAsMap); err != nil {
		t.Fatal(err)
	}

	if _, hasUrls := multiAsMap["urls"]; !hasUrls {
		t.Fatalf("expected multi payload to include urls field, got %#v", multiAsMap)
	}
	expectDefaultConfigs(multiAsMap)
}
