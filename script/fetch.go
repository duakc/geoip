package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
	"go4.org/netipx"
)

const (
	output = "data/ip"

	source = "source.json"

	// IPinfo free "IP to Country" database, served as a raw .mmdb file.
	ipinfoURL = "https://ipinfo.io/data/free/country.mmdb"
)

func main() {
	config, err := unmarshalSource(source)
	if err != nil {
		fatalf("failed to unmarshal source: %v", err)
	}

	if err = os.MkdirAll(output, 0755); err != nil {
		fatalf("failed to mkdir output: %v", err)
	}

	client := &http.Client{}

	// source.json has the highest priority: collect its codes so that MaxMind
	// data for the same code is ignored even when MaxMind has it.
	overrides := make(map[string]struct{}, len(config.Geoip))
	for _, c := range config.Geoip {
		overrides[c.Code] = struct{}{}
	}

	// Generate every country from the IPinfo mmdb, skipping codes overridden
	// by source.json.
	if err = fetchIPinfo(client, output, overrides); err != nil {
		fatalf("failed to fetch ipinfo: %v", err)
	}

	// Write source.json data on top (overrides + new additions).
	if err = fetchAll(client, config, output); err != nil {
		fatalf("failed to fetch source: %v", err)
	}
}

// fetchIPinfo downloads the IPinfo Country mmdb, then writes a per-country
// CIDR file for every country, skipping any code present in skip.
func fetchIPinfo(client *http.Client, output string, skip map[string]struct{}) error {
	token := os.Getenv("IPINFO_TOKEN")
	if token == "" {
		return fmt.Errorf("IPINFO_TOKEN is not set")
	}

	u := fmt.Sprintf("%s?token=%s", ipinfoURL, url.QueryEscape(token))
	data, err := doFetch(client, u)
	if err != nil {
		return err
	}

	reader, err := maxminddb.OpenBytes(data)
	if err != nil {
		return fmt.Errorf("open mmdb: %w", err)
	}
	defer reader.Close()

	// country code -> aggregated IP set.
	builders := make(map[string]*netipx.IPSetBuilder)
	for result := range reader.Networks() {
		var rec struct {
			Country string `maxminddb:"country"`
		}
		if err = result.Decode(&rec); err != nil {
			return fmt.Errorf("decode record: %w", err)
		}

		if rec.Country == "" {
			continue
		}
		code := strings.ToLower(rec.Country)
		if _, skipped := skip[code]; skipped {
			continue
		}

		b := builders[code]
		if b == nil {
			b = &netipx.IPSetBuilder{}
			builders[code] = b
		}
		b.AddPrefix(result.Prefix())
	}

	codes := make([]string, 0, len(builders))
	for code := range builders {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, code := range codes {
		set, err := builders[code].IPSet()
		if err != nil {
			return fmt.Errorf("%s: %w", code, err)
		}
		if err = writeIPSet(filepath.Join(output, code), set); err != nil {
			return err
		}
	}
	return nil
}

// writeIPSet writes the set's prefixes as a newline-separated CIDR list.
func writeIPSet(path string, set *netipx.IPSet) error {
	var buf bytes.Buffer
	for _, p := range set.Prefixes() {
		buf.WriteString(p.String())
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func fetchAll(client *http.Client, config geoipConfig, output string) error {
	for _, c := range config.Geoip {
		var (
			data []byte
			err  error
		)
		switch {
		case len(c.Prefixes) > 0:
			// Inline constant data, no fetch.
			data = []byte(strings.Join(c.Prefixes, "\n") + "\n")
		case c.Source != "":
			data, err = doFetch(client, c.Source)
		default:
			data, err = fetchV4V6(client, c.SourceV4, c.SourceV6)
		}

		if err != nil {
			return err
		}

		if err = os.WriteFile(filepath.Join(output, c.Code), data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func fetchV4V6(c *http.Client, v4, v6 string) ([]byte, error) {
	v4Data, err := doFetch(c, v4)
	if err != nil {
		return nil, fmt.Errorf("v4: %v", err)
	}
	if len(v4Data) == 0 {
		return nil, fmt.Errorf("v4: no data")
	}
	v6Data, err := doFetch(c, v6)
	if err != nil {
		return nil, fmt.Errorf("v6: %v", err)
	}
	if len(v6Data) == 0 {
		return nil, fmt.Errorf("v6: no data")
	}

	// LF or CRLF
	if !bytes.HasSuffix(v4Data, []byte("\n")) {
		var nextline = "\n" // default is LF

		// detect if this file is LF or CRLF
		for i := 1; i < len(v4Data); i++ {
			char := v4Data[i]
			if char == '\n' && v4Data[i-1] == '\r' {
				// CRLF
				nextline = "\r\n"
				break
			} else if char == '\n' {
				// LF
				break
			}
		}
		v4Data = append(v4Data, []byte(nextline)...)
	}

	return append(v4Data, v6Data...), nil
}

func doFetch(c *http.Client, u string) ([]byte, error) {
	if c == nil {
		panic("nil client")
	}
	if u == "" {
		return nil, fmt.Errorf("empty url")
	}
	request, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	var buffer bytes.Buffer
	if resp.ContentLength > 0 {
		buffer.Grow(int(resp.ContentLength))
	}
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func unmarshalSource(v string) (geoipConfig, error) {
	file, err := os.Open(v)
	if err != nil {
		return geoipConfig{}, err
	}
	defer file.Close()
	var config geoipConfig
	err = json.NewDecoder(file).Decode(&config)
	if err != nil {
		return geoipConfig{}, err
	}
	return config, nil
}

type geoipConfig struct {
	Geoip []struct {
		Code     string `json:"code"`
		Source   string `json:"source"`
		SourceV4 string `json:"source_v4"`
		SourceV6 string `json:"source_v6"`
		// Prefixes is inline constant data (CIDR per element), used in place of
		// fetching from a URL — e.g. the fixed private/reserved ranges.
		Prefixes []string `json:"prefixes"`
	} `json:"geoip"`
}

func (c *geoipConfig) UnmarshalJSON(b []byte) error {
	type _geoipConfig geoipConfig
	dummyConfig := _geoipConfig{}
	err := json.Unmarshal(b, &dummyConfig)
	if err != nil {
		return err
	}
	if len(dummyConfig.Geoip) == 0 {
		return fmt.Errorf("no geoip config")
	}
	for idx, geoip := range dummyConfig.Geoip {
		if geoip.Code == "" {
			return fmt.Errorf("[%d]: no code", idx)
		}
		if len(geoip.Prefixes) > 0 {
			continue
		}
		if geoip.Source == "" {
			if geoip.SourceV4 == "" && geoip.SourceV6 == "" {
				return fmt.Errorf("[%d:%s]: no source", idx, geoip.Code)
			}
			if geoip.SourceV4 != "" && geoip.SourceV6 == "" {
				warnf("[%d:%s]: missing v6 source", idx, geoip.Code)
			}
			if geoip.SourceV6 != "" && geoip.SourceV4 == "" {
				warnf("[%d:%s]: missing v4 source", idx, geoip.Code)
			}
		}
	}

	*c = geoipConfig(dummyConfig)
	return nil
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "(fatal) "+format+"\n", args...)
	os.Exit(1)
}

func warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "(warn) "+format+"\n", args...)
}
