package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var (
	LISTEN_IP         string = ""
	LISTEN_PORT       int    = 8000
	CRAWL4AI_ENDPOINT        = "http://crawl4ai:11235/md"
)

func ReadEnvironment() {
	portStr := os.Getenv("LISTEN_PORT")
	port, err := strconv.Atoi(portStr)
	if err == nil {
		LISTEN_PORT = port
	}

	ip := os.Getenv("LISTEN_IP")
	if ip != "" {
		LISTEN_IP = ip
	}

	endpoint := os.Getenv("CRAWL4AI_ENDPOINT")
	if endpoint != "" {
		CRAWL4AI_ENDPOINT = endpoint
	}
}

// For the openwebui-facing endpoint
type Request struct {
	Urls []string `json:"urls"`
	Url  string   `json:"url,omitempty"`
}

type SuccessResponseItem struct {
	PageContent string            `json:"page_content"`
	Metadata    map[string]string `json:"metadata"`
}
type SuccessResponse []SuccessResponseItem

type ErrorResponse struct {
	ErrorName string `json:"error"`
	Detail    string `json:"detail"`
}

func errorResponseFromError(name string, err error) ErrorResponse {
	return ErrorResponse{
		ErrorName: name,
		Detail:    err.Error(),
	}
}

func jsonEncodeInfallible(object any) []byte {
	encoded, err := json.Marshal(object)
	if err != nil {
		panic(err)
	}
	return encoded
}

func decodeResults(payload any) []map[string]any {
	convertList := func(rawList any) ([]map[string]any, bool) {
		list, isList := rawList.([]any)
		if !isList {
			return nil, false
		}

		ret := []map[string]any{}
		for _, item := range list {
			itemMap, isMap := item.(map[string]any)
			if isMap {
				ret = append(ret, itemMap)
			}
		}

		return ret, true
	}

	switch data := payload.(type) {
	case []any:
		results, ok := convertList(data)
		if ok {
			return results
		}
	case map[string]any:
		results, hasResults := convertList(data["results"])
		if hasResults {
			return results
		}

		results, hasData := convertList(data["data"])
		if hasData {
			return results
		}

		return []map[string]any{data}
	}

	return nil
}

func stringMapFromAny(data any) map[string]string {
	ret := map[string]string{}

	dataMap, isMap := data.(map[string]any)
	if isMap {
		for key, value := range dataMap {
			valueString, isString := value.(string)
			if isString && valueString != "" {
				ret[key] = valueString
			}
		}
	}

	return ret
}

func extractMarkdown(result map[string]any) string {
	// Prefer filtered/fit markdown, but gracefully fall back to raw markdown/content.
	for _, key := range []string{"filtered_markdown", "fit_markdown", "markdown", "raw_markdown", "content", "page_content"} {
		value, exists := result[key]
		if !exists {
			continue
		}

		valueString, isString := value.(string)
		if isString && valueString != "" {
			return valueString
		}
	}

	markdownField, markdownExists := result["markdown"]
	if !markdownExists {
		return ""
	}

	markdownMap, isMap := markdownField.(map[string]any)
	if !isMap {
		return ""
	}

	for _, key := range []string{"filtered_markdown", "fit_markdown", "markdown", "raw_markdown"} {
		value, exists := markdownMap[key]
		if !exists {
			continue
		}

		valueString, isString := value.(string)
		if isString && valueString != "" {
			return valueString
		}
	}

	return ""
}

func normalizeRequestUrls(requestData Request) []string {
	ret := []string{}
	for _, url := range requestData.Urls {
		if url != "" {
			ret = append(ret, url)
		}
	}

	if requestData.Url != "" {
		ret = append(ret, requestData.Url)
	}

	return ret
}

func crawlRequestPayloadCandidates(urls []string) [][]byte {
	if len(urls) == 1 {
		return [][]byte{
			jsonEncodeInfallible(Request{Url: urls[0]}),
			jsonEncodeInfallible(Request{Urls: urls}),
		}
	}

	return [][]byte{
		jsonEncodeInfallible(Request{Urls: urls}),
	}
}

func previewResponseBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	const maxLen = 300
	if len(text) > maxLen {
		return text[:maxLen] + "..."
	}
	return text
}

type CrawlAPICallResult struct {
	Data        any
	StatusCode  int
	BodyPreview string
	Err         error
}

func callCrawlAPI(payload []byte) CrawlAPICallResult {
	req, err := http.NewRequest("POST", CRAWL4AI_ENDPOINT, bytes.NewReader(payload))
	if err != nil {
		return CrawlAPICallResult{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	crawlResponse, err := http.DefaultClient.Do(req)
	if err != nil {
		return CrawlAPICallResult{Err: err}
	}
	defer crawlResponse.Body.Close()

	body, err := io.ReadAll(crawlResponse.Body)
	if err != nil {
		return CrawlAPICallResult{Err: err}
	}

	if crawlResponse.StatusCode != 200 {
		return CrawlAPICallResult{
			StatusCode:  crawlResponse.StatusCode,
			BodyPreview: previewResponseBody(body),
		}
	}

	var crawlData any
	err = json.Unmarshal(body, &crawlData)
	if err != nil {
		return CrawlAPICallResult{
			StatusCode: crawlResponse.StatusCode,
			Err:        fmt.Errorf("invalid json received from crawl api"),
		}
	}

	return CrawlAPICallResult{
		StatusCode: crawlResponse.StatusCode,
		Data:       crawlData,
	}
}

func callCrawlAPIWithFallback(urls []string) CrawlAPICallResult {
	var lastResult CrawlAPICallResult
	for _, payload := range crawlRequestPayloadCandidates(urls) {
		result := callCrawlAPI(payload)
		if result.Err == nil && result.StatusCode == 200 {
			return result
		}
		lastResult = result
	}
	return lastResult
}

func CrawlEndpoint(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")

	if request.Method != "POST" {
		response.WriteHeader(405)
		resp := ErrorResponse{ErrorName: "method not allowed"}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("405 method not allowed :: %s\n", request.RemoteAddr)
		return
	}

	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		response.WriteHeader(400)
		resp := ErrorResponse{ErrorName: "content type must be application/json"}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("400 invalid content type :: %s\n", request.RemoteAddr)
		return
	}

	var requestData Request
	err := json.NewDecoder(request.Body).Decode(&requestData)
	if err != nil {
		response.WriteHeader(400)
		resp := errorResponseFromError("invalid json", err)
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("400 invalid json :: %s\n", request.RemoteAddr)
		return
	}

	requestUrls := normalizeRequestUrls(requestData)
	if len(requestUrls) == 0 {
		response.WriteHeader(400)
		resp := ErrorResponse{ErrorName: "invalid json", Detail: "request must include `url` or `urls`"}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("400 invalid json :: %s\n", request.RemoteAddr)
		return
	}

	log.Printf("Request to crawl %s from %s\n", requestUrls, request.RemoteAddr)

	crawlAPICallResult := callCrawlAPIWithFallback(requestUrls)
	if crawlAPICallResult.Err != nil {
		response.WriteHeader(502)
		resp := ErrorResponse{ErrorName: "bad gateway", Detail: crawlAPICallResult.Err.Error()}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("502 bad gateway - crawl api call failed: %v :: %s\n", crawlAPICallResult.Err, request.RemoteAddr)
		return
	}

	if crawlAPICallResult.StatusCode != 200 {
		errorDetail := fmt.Sprintf("crawl api returned status %d", crawlAPICallResult.StatusCode)
		if crawlAPICallResult.BodyPreview != "" {
			errorDetail += ": " + crawlAPICallResult.BodyPreview
		}

		response.WriteHeader(502)
		resp := ErrorResponse{ErrorName: "bad gateway", Detail: errorDetail}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf(
			"502 bad gateway - crawl api status=%d body=%q :: %s\n",
			crawlAPICallResult.StatusCode,
			crawlAPICallResult.BodyPreview,
			request.RemoteAddr,
		)
		return
	}

	crawlData := crawlAPICallResult.Data

	crawlResults := decodeResults(crawlData)
	if crawlResults == nil {
		response.WriteHeader(502)
		resp := ErrorResponse{ErrorName: "bad gateway", Detail: "invalid json structure received from crawl api"}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("502 bad gateway - invalid json structure from crawl api :: %s\n", request.RemoteAddr)
		return
	}

	ret := SuccessResponse{}
	for _, result := range crawlResults {
		metadata := stringMapFromAny(result["metadata"])

		url, hasUrl := result["url"].(string)
		if hasUrl && url != "" {
			metadata["source"] = url
		}

		ret = append(ret, SuccessResponseItem{
			PageContent: extractMarkdown(result),
			Metadata:    metadata,
		})
	}

	response.WriteHeader(200)
	response.Write(jsonEncodeInfallible(ret))
	log.Printf("200 :: %s\n", request.RemoteAddr)
}

func main() {
	ReadEnvironment()

	http.HandleFunc("/crawl", CrawlEndpoint)
	http.HandleFunc("/md", CrawlEndpoint)

	listenAddress := fmt.Sprintf("%s:%d", LISTEN_IP, LISTEN_PORT)
	log.Printf("Listening on %s\n", listenAddress)

	err := http.ListenAndServe(listenAddress, nil)
	if err != nil {
		log.Println(err)
	}
}
