package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

	log.Printf("Request to crawl %s from %s\n", requestData.Urls, request.RemoteAddr)

	req, err := http.NewRequest("POST", CRAWL4AI_ENDPOINT, bytes.NewReader(jsonEncodeInfallible(requestData)))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")

	crawlResponse, err := http.DefaultClient.Do(req)
	if err != nil || crawlResponse.StatusCode != 200 {
		response.WriteHeader(502)
		resp := ErrorResponse{ErrorName: "bad gateway"}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("502 bad gateway :: %s\n", request.RemoteAddr)
		return
	}
	defer crawlResponse.Body.Close()

	var crawlData any
	err = json.NewDecoder(crawlResponse.Body).Decode(&crawlData)
	if err != nil {
		response.WriteHeader(502)
		resp := ErrorResponse{ErrorName: "bad gateway", Detail: "invalid json received from crawl api"}
		response.Write(jsonEncodeInfallible(resp))
		log.Printf("502 bad gateway - invalid json from crawl api :: %s\n", request.RemoteAddr)
		return
	}

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
