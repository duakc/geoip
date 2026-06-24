package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

const (
	output = "data/ip"

	source = "source.json"
)

func main() {
	config, err := unmarshalSource(source)
	if err != nil {
		fatalf("failed to unmarshal source: %v", err)
	}
	err = fetchAll(config, output)
	if err != nil {
		fatalf("failed to fetch: %v", err)
	}
}

func fetchAll(config geoipConfig, output string) error {
	err := os.MkdirAll(output, 0755)
	if err != nil {
		return err
	}

	client := &http.Client{}
	for _, c := range config.Geoip {
		var (
			data []byte
			err  error
		)
		if c.Source != "" {
			data, err = doFetch(client, c.Source)
		} else {
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

func fatalf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(os.Stderr, "(fatal) "+format+"\n", args...)
	os.Exit(1)
}

func warnf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(os.Stderr, "(warn) "+format+"\n", args...)
}
