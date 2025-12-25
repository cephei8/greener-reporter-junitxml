package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
)

const (
	ingressEndpointFlag = "ingress-endpoint"
	ingressAPIKeyFlag   = "ingress-api-key"
	xmlFileFlag         = "xml-file"
)

type TestSuites struct {
	XMLName    xml.Name    `xml:"testsuites"`
	TestSuites []TestSuite `xml:"testsuite"`
}

type TestSuite struct {
	Name      string     `xml:"name,attr"`
	Tests     int        `xml:"tests,attr"`
	Failures  int        `xml:"failures,attr"`
	Errors    int        `xml:"errors,attr"`
	Skipped   int        `xml:"skipped,attr"`
	Time      string     `xml:"time,attr"`
	Timestamp string     `xml:"timestamp,attr"`
	TestCases []TestCase `xml:"testcase"`
}

type TestCase struct {
	Name      string   `xml:"name,attr"`
	Classname string   `xml:"classname,attr"`
	Time      string   `xml:"time,attr"`
	Failure   *Failure `xml:"failure,omitempty"`
	Error     *Error   `xml:"error,omitempty"`
	Skipped   *Skipped `xml:"skipped,omitempty"`
}

type Failure struct {
	Message string `xml:"message,attr,omitempty"`
	Type    string `xml:"type,attr,omitempty"`
	Content string `xml:",chardata"`
}

type Error struct {
	Message string `xml:"message,attr,omitempty"`
	Type    string `xml:"type,attr,omitempty"`
	Content string `xml:",chardata"`
}

type Skipped struct {
	Message string `xml:"message,attr,omitempty"`
}

type SessionRequest struct {
	Id          string         `json:"id,omitempty"`
	Description string         `json:"description,omitempty"`
	Baggage     map[string]any `json:"baggage,omitempty"`
}

type SessionResponse struct {
	Id string `json:"id"`
}

type TestcaseRequest struct {
	SessionId         string         `json:"sessionId"`
	TestcaseName      string         `json:"testcaseName"`
	TestcaseClassname string         `json:"testcaseClassname,omitempty"`
	TestcaseFile      string         `json:"testcaseFile,omitempty"`
	Testsuite         string         `json:"testsuite,omitempty"`
	Status            string         `json:"status"`
	Output            string         `json:"output,omitempty"`
	Baggage           map[string]any `json:"baggage,omitempty"`
}

type TestcasesRequest struct {
	Testcases []TestcaseRequest `json:"testcases"`
}

type Reporter struct {
	endpoint  string
	apiKey    string
	sessionId string
	client    *http.Client
}

func NewReporter(endpoint, apiKey string) *Reporter {
	return &Reporter{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		apiKey:   apiKey,
		client:   &http.Client{},
	}
}

func (r *Reporter) createSession() error {
	req := SessionRequest{
		Description: "JUnit XML test report",
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal session request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", r.endpoint+"/api/v1/ingress/sessions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create session request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", r.apiKey)

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send session request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create session failed: status=%d body=%s", resp.StatusCode, string(bodyBytes))
	}

	var sessionResp SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil {
		return fmt.Errorf("decode session response: %w", err)
	}

	r.sessionId = sessionResp.Id
	log.Printf("Created session: %s\n", r.sessionId)
	return nil
}

func (r *Reporter) submitResults(testsuites TestSuites) error {
	var testcases []TestcaseRequest

	for _, suite := range testsuites.TestSuites {
		for _, tc := range suite.TestCases {
			status := "pass"
			var output string

			if tc.Failure != nil {
				status = "fail"
				output = fmt.Sprintf("Failure: %s\n%s", tc.Failure.Message, tc.Failure.Content)
			} else if tc.Error != nil {
				status = "error"
				output = fmt.Sprintf("Error: %s\n%s", tc.Error.Message, tc.Error.Content)
			} else if tc.Skipped != nil {
				status = "skip"
				output = tc.Skipped.Message
			}

			testcases = append(testcases, TestcaseRequest{
				SessionId:         r.sessionId,
				TestcaseName:      tc.Name,
				TestcaseClassname: tc.Classname,
				Testsuite:         suite.Name,
				Status:            status,
				Output:            output,
			})
		}
	}

	if len(testcases) == 0 {
		log.Println("No test results to submit")
		return nil
	}

	req := TestcasesRequest{
		Testcases: testcases,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal testcases request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", r.endpoint+"/api/v1/ingress/testcases", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create testcases request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", r.apiKey)

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send testcases request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("submit testcases failed: status=%d body=%s", resp.StatusCode, string(bodyBytes))
	}

	log.Printf("Submitted %d test results\n", len(testcases))
	return nil
}

func run(ctx context.Context, c *cli.Command) error {
	endpoint := c.String(ingressEndpointFlag)
	apiKey := c.String(ingressAPIKeyFlag)
	xmlFile := c.String(xmlFileFlag)

	reporter := NewReporter(endpoint, apiKey)

	if err := reporter.createSession(); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	var xmlData []byte
	var err error

	if xmlFile == "-" {
		xmlData, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read from stdin: %w", err)
		}
	} else {
		xmlData, err = os.ReadFile(xmlFile)
		if err != nil {
			return fmt.Errorf("read file %s: %w", xmlFile, err)
		}
	}

	var testsuites TestSuites
	if err := xml.Unmarshal(xmlData, &testsuites); err != nil {
		return fmt.Errorf("parse XML: %w", err)
	}

	if err := reporter.submitResults(testsuites); err != nil {
		return fmt.Errorf("submit results: %w", err)
	}

	return nil
}

func main() {
	cmd := &cli.Command{
		Name:  "greener-reporter-junitxml",
		Usage: "Report JUnit XML test results to Greener",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     ingressEndpointFlag,
				Usage:    "Greener ingress endpoint",
				Sources:  cli.EnvVars("GREENER_INGRESS_ENDPOINT"),
				Required: true,
			},
			&cli.StringFlag{
				Name:     ingressAPIKeyFlag,
				Usage:    "Greener ingress API key",
				Sources:  cli.EnvVars("GREENER_INGRESS_API_KEY"),
				Required: true,
			},
			&cli.StringFlag{
				Name:     xmlFileFlag,
				Usage:    "Path to JUnit XML file (use '-' for stdin)",
				Aliases:  []string{"f"},
				Required: true,
			},
		},
		Action: run,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
