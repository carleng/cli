package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/henvic/httpretty"
)

// ClientOption represents an argument to NewClient
type ClientOption = func(http.RoundTripper) http.RoundTripper

// NewClient initializes a Client
func NewClient(opts ...ClientOption) *Client {
	tr := http.DefaultTransport
	for _, opt := range opts {
		tr = opt(tr)
	}
	http := &http.Client{Transport: tr}
	client := &Client{http: http}
	return client
}

// AddHeader turns a RoundTripper into one that adds a request header
func AddHeader(name, value string) ClientOption {
	return func(tr http.RoundTripper) http.RoundTripper {
		return &funcTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			req.Header.Add(name, value)
			return tr.RoundTrip(req)
		}}
	}
}

// AddHeaderFunc is an AddHeader that gets the string value from a function
func AddHeaderFunc(name string, value func() string) ClientOption {
	return func(tr http.RoundTripper) http.RoundTripper {
		return &funcTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			req.Header.Add(name, value())
			return tr.RoundTrip(req)
		}}
	}
}

// VerboseLog enables request/response logging within a RoundTripper
func VerboseLog(out io.Writer, logTraffic bool, colorize bool) ClientOption {
	logger := &httpretty.Logger{
		Time:           true,
		TLS:            false,
		Colors:         colorize,
		RequestHeader:  logTraffic,
		RequestBody:    logTraffic,
		ResponseHeader: logTraffic,
		ResponseBody:   logTraffic,
		Formatters:     []httpretty.Formatter{&httpretty.JSONFormatter{}},
	}
	logger.SetOutput(out)
	logger.SetBodyFilter(func(h http.Header) (skip bool, err error) {
		return !inspectableMIMEType(h.Get("Content-Type")), nil
	})
	return logger.RoundTripper
}

// ReplaceTripper substitutes the underlying RoundTripper with a custom one
func ReplaceTripper(tr http.RoundTripper) ClientOption {
	return func(http.RoundTripper) http.RoundTripper {
		return tr
	}
}

var issuedScopesWarning bool

// CheckScopes checks whether an OAuth scope is present in a response
func CheckScopes(wantedScope string, cb func(string) error) ClientOption {
	return func(tr http.RoundTripper) http.RoundTripper {
		return &funcTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			res, err := tr.RoundTrip(req)
			if err != nil || res.StatusCode > 299 || issuedScopesWarning {
				return res, err
			}

			appID := res.Header.Get("X-Oauth-Client-Id")
			hasScopes := strings.Split(res.Header.Get("X-Oauth-Scopes"), ",")

			hasWanted := false
			for _, s := range hasScopes {
				if wantedScope == strings.TrimSpace(s) {
					hasWanted = true
					break
				}
			}

			if !hasWanted {
				if err := cb(appID); err != nil {
					return res, err
				}
				issuedScopesWarning = true
			}

			return res, nil
		}}
	}
}

type funcTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (tr funcTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return tr.roundTrip(req)
}

// Client facilitates making HTTP requests to the GitHub API
type Client struct {
	http *http.Client
}

type graphQLResponse struct {
	Data   interface{}
	Errors []GraphQLError
}

// GraphQLError is a single error returned in a GraphQL response
type GraphQLError struct {
	Type    string
	Path    []string
	Message string
}

// GraphQLErrorResponse contains errors returned in a GraphQL response
type GraphQLErrorResponse struct {
	Errors []GraphQLError
}

func (gr GraphQLErrorResponse) Error() string {
	errorMessages := make([]string, 0, len(gr.Errors))
	for _, e := range gr.Errors {
		errorMessages = append(errorMessages, e.Message)
	}
	return fmt.Sprintf("graphql error: '%s'", strings.Join(errorMessages, ", "))
}

// GraphQL performs a GraphQL request and parses the response
func (c Client) GraphQL(query string, variables map[string]interface{}, data interface{}) error {
	url := "https://api.github.com/graphql"
	reqBody, err := json.Marshal(map[string]interface{}{"query": query, "variables": variables})
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return handleResponse(resp, data)
}

// REST performs a REST request and parses the response.
func (c Client) REST(method string, p string, body io.Reader, data interface{}) error {
	url := "https://api.github.com/" + p
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !success {
		return handleHTTPError(resp)
	}

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	err = json.Unmarshal(b, &data)
	if err != nil {
		return err
	}

	return nil
}

// DirectRequest is a low-level interface to making generic API requests
func (c Client) DirectRequest(method string, p string, params interface{}, headers []string) (*http.Response, error) {
	url := "https://api.github.com/" + p
	var body io.Reader
	var bodyIsJSON bool
	isGraphQL := p == "graphql"

	switch pp := params.(type) {
	case map[string]interface{}:
		if strings.EqualFold(method, "GET") {
			url = addQuery(url, pp)
		} else {
			if isGraphQL {
				pp = groupGraphQLVariables(pp)
			}
			b, err := json.Marshal(pp)
			if err != nil {
				return nil, fmt.Errorf("error serializing parameters: %w", err)
			}
			body = bytes.NewBuffer(b)
			bodyIsJSON = true
		}
	case io.Reader:
		body = pp
	default:
		return nil, fmt.Errorf("unrecognized parameters type: %v", params)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	if bodyIsJSON {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for _, h := range headers {
		idx := strings.IndexRune(h, ':')
		if idx == -1 {
			return nil, fmt.Errorf("header %q requires a value separated by ':'", h)
		}
		req.Header.Set(h[0:idx], strings.TrimSpace(h[idx+1:]))
	}

	return c.http.Do(req)
}

func groupGraphQLVariables(params map[string]interface{}) map[string]interface{} {
	topLevel := make(map[string]interface{})
	variables := make(map[string]interface{})

	for key, val := range params {
		switch key {
		case "query":
			topLevel[key] = val
		default:
			variables[key] = val
		}
	}

	if len(variables) > 0 {
		topLevel["variables"] = variables
	}
	return topLevel
}

func addQuery(path string, params map[string]interface{}) string {
	if len(params) == 0 {
		return path
	}

	query := url.Values{}
	for key, value := range params {
		switch v := value.(type) {
		case string:
			query.Add(key, v)
		case []byte:
			query.Add(key, string(v))
		case nil:
			query.Add(key, "")
		case int:
			query.Add(key, fmt.Sprintf("%d", v))
		case bool:
			query.Add(key, fmt.Sprintf("%v", v))
		default:
			panic(fmt.Sprintf("unknown type %v", v))
		}
	}

	sep := "?"
	if strings.ContainsRune(path, '?') {
		sep = "&"
	}
	return path + sep + query.Encode()
}

func handleResponse(resp *http.Response, data interface{}) error {
	success := resp.StatusCode >= 200 && resp.StatusCode < 300

	if !success {
		return handleHTTPError(resp)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	gr := &graphQLResponse{Data: data}
	err = json.Unmarshal(body, &gr)
	if err != nil {
		return err
	}

	if len(gr.Errors) > 0 {
		return &GraphQLErrorResponse{Errors: gr.Errors}
	}
	return nil
}

func handleHTTPError(resp *http.Response) error {
	var message string
	var parsedBody struct {
		Message string `json:"message"`
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	err = json.Unmarshal(body, &parsedBody)
	if err != nil {
		message = string(body)
	} else {
		message = parsedBody.Message
	}

	return fmt.Errorf("http error, '%s' failed (%d): '%s'", resp.Request.URL, resp.StatusCode, message)
}

var jsonTypeRE = regexp.MustCompile(`[/+]json($|;)`)

func inspectableMIMEType(t string) bool {
	return strings.HasPrefix(t, "text/") || jsonTypeRE.MatchString(t)
}
