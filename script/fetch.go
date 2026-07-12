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

	// sourceIPinfo is the sentinel value for source_v4/source_v6: instead of a
	// URL, pull that address family's prefixes for the entry's country code
	// straight from the IPinfo mmdb.
	sourceIPinfo = "ipinfo"
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

	// source.json has the highest priority: collect its codes so that IPinfo
	// data for the same code is ignored even when IPinfo has it.
	overrides := make(map[string]struct{}, len(config.Geoip))
	for _, c := range config.Geoip {
		overrides[c.Code] = struct{}{}
	}

	// Download and parse the IPinfo mmdb once; both the "generate every
	// country" pass and any source.json entry using the "ipinfo" sentinel read
	// from it.
	db, err := parseIPinfo(client)
	if err != nil {
		fatalf("failed to parse ipinfo: %v", err)
	}

	// Generate every country from the IPinfo mmdb, skipping codes overridden
	// by source.json.
	if err = writeAllCountries(db, output, overrides); err != nil {
		fatalf("failed to write ipinfo countries: %v", err)
	}

	// Write source.json data on top (overrides + new additions).
	if err = fetchAll(client, config, output, db); err != nil {
		fatalf("failed to fetch source: %v", err)
	}
}

// ipinfoDB holds per-country IP sets parsed from the IPinfo mmdb, split by
// address family so that source.json can pull v4 or v6 independently via the
// "ipinfo" sentinel.
type ipinfoDB struct {
	v4 map[string]*netipx.IPSetBuilder
	v6 map[string]*netipx.IPSetBuilder
}

// parseIPinfo downloads the IPinfo Country mmdb and buckets every network into
// per-country, per-family IP sets.
func parseIPinfo(client *http.Client) (*ipinfoDB, error) {
	token := os.Getenv("IPINFO_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("IPINFO_TOKEN is not set")
	}

	u := fmt.Sprintf("%s?token=%s", ipinfoURL, url.QueryEscape(token))
	data, err := doFetch(client, u)
	if err != nil {
		return nil, err
	}

	reader, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, fmt.Errorf("open mmdb: %w", err)
	}
	defer reader.Close()

	db := &ipinfoDB{
		v4: make(map[string]*netipx.IPSetBuilder),
		v6: make(map[string]*netipx.IPSetBuilder),
	}
	for result := range reader.Networks() {
		var rec struct {
			Country string `maxminddb:"country"`
		}
		if err = result.Decode(&rec); err != nil {
			return nil, fmt.Errorf("decode record: %w", err)
		}

		if rec.Country == "" {
			continue
		}
		code := strings.ToLower(rec.Country)
		prefix := result.Prefix()

		m := db.v6
		if prefix.Addr().Is4() {
			m = db.v4
		}
		b := m[code]
		if b == nil {
			b = &netipx.IPSetBuilder{}
			m[code] = b
		}
		b.AddPrefix(prefix)
	}
	return db, nil
}

// prefixes returns the aggregated CIDR list for a country code and family
// ("v4" or "v6") as newline-separated bytes.
func (db *ipinfoDB) prefixes(code, family string) ([]byte, error) {
	var m map[string]*netipx.IPSetBuilder
	switch family {
	case "v4":
		m = db.v4
	case "v6":
		m = db.v6
	default:
		return nil, fmt.Errorf("ipinfo: unknown family %q", family)
	}

	b := m[code]
	if b == nil {
		return nil, fmt.Errorf("ipinfo: no %s data for %q", family, code)
	}
	set, err := b.IPSet()
	if err != nil {
		return nil, fmt.Errorf("ipinfo: %s %s: %w", code, family, err)
	}
	return ipSetBytes(set), nil
}

// writeAllCountries writes a per-country CIDR file (v4 then v6) for every
// country in the IPinfo mmdb, skipping any code present in skip.
func writeAllCountries(db *ipinfoDB, output string, skip map[string]struct{}) error {
	seen := make(map[string]struct{}, len(db.v4)+len(db.v6))
	for code := range db.v4 {
		seen[code] = struct{}{}
	}
	for code := range db.v6 {
		seen[code] = struct{}{}
	}

	codes := make([]string, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, code := range codes {
		if _, skipped := skip[code]; skipped {
			continue
		}

		var buf bytes.Buffer
		for _, family := range []string{"v4", "v6"} {
			data, err := db.prefixes(code, family)
			if err != nil {
				// A country may have only one address family.
				continue
			}
			buf.Write(data)
		}
		if err := os.WriteFile(filepath.Join(output, code), buf.Bytes(), 0644); err != nil {
			return err
		}
	}
	return nil
}

// ipSetBytes renders the set's prefixes as a newline-separated CIDR list.
func ipSetBytes(set *netipx.IPSet) []byte {
	var buf bytes.Buffer
	for _, p := range set.Prefixes() {
		buf.WriteString(p.String())
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func fetchAll(client *http.Client, config geoipConfig, output string, db *ipinfoDB) error {
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
			data, err = fetchV4V6(client, db, c.Code, c.SourceV4, c.SourceV6)
		}

		if err != nil {
			return err
		}

		data = skipNLine(data, c.SkipLineHeader)

		if err = os.WriteFile(filepath.Join(output, c.Code), data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// resolveSource returns the CIDR data for one address family: from the IPinfo
// mmdb when src is the "ipinfo" sentinel, otherwise by fetching src as a URL.
func resolveSource(c *http.Client, db *ipinfoDB, code, family, src string) ([]byte, error) {
	if src == sourceIPinfo {
		return db.prefixes(code, family)
	}
	return doFetch(c, src)
}

func fetchV4V6(c *http.Client, db *ipinfoDB, code, v4, v6 string) ([]byte, error) {
	v4Data, err := resolveSource(c, db, code, "v4", v4)
	if err != nil {
		return nil, fmt.Errorf("v4: %v", err)
	}
	if len(v4Data) == 0 {
		return nil, fmt.Errorf("v4: no data")
	}
	v6Data, err := resolveSource(c, db, code, "v6", v6)
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

		SkipLineHeader int `json:"skip_line_header"`
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

func skipNLine(data []byte, n int) []byte {
	for i := 0; i < n && len(data) >= 1; i++ {
		nn := bytes.IndexByte(data, '\n')
		if nn < 0 || nn == len(data)-1 {
			break
		}
		data = data[nn+1:]
	}
	return data
}
