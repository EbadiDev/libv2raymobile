package libv2raymobile

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	// core "github.com/v2fly/v2ray-core/v5"
	// core "github.com/xtls/xray-core/core"
	v2net "github.com/GFW-knocker/Xray-core/common/net"
	core "github.com/GFW-knocker/Xray-core/core"

	// serial "github.com/v2fly/v2ray-core/v5/infra/conf/serial"
	// serial "github.com/xtls/xray-core/infra/conf/serial"
	serial "github.com/GFW-knocker/Xray-core/infra/conf/serial"
	// _ "github.com/v2fly/v2ray-core/v5/main/distro/all"
	// _ "github.com/xtls/xray-core/main/distro/all"
	_ "github.com/GFW-knocker/Xray-core/main/distro/all"
)

type CoreManager struct {
	inst      *core.Instance
	shouldOff chan int
}

func (m *CoreManager) runConfigSync(confPath string) {
	bs := readFileAsBytes(confPath)

	r := bytes.NewReader(bs)
	config, err := serial.LoadJSONConfig(r)
	if err != nil {
		fmt.Println(err)
		return
	}

	if m.inst != nil {
		fmt.Println("m.inst != nil")
		return
	}
	m.inst, err = core.New(config)
	if err != nil {
		log.Println(err)
		return
	}

	err = m.inst.Start()
	if err != nil {
		log.Println(err)
		return
	}

	// Explicitly triggering GC to remove garbage from config loading.
	runtime.GC()

	{
		m.shouldOff = make(chan int, 1)
		<-m.shouldOff
	}
}

func (m *CoreManager) RunConfig(confPath string) {
	go m.runConfigSync(confPath)
}

func readFileAsBytes(filePath string) (bs []byte) {
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer file.Close()

	// Get the file size
	stat, err := file.Stat()
	if err != nil {
		fmt.Println(err)
		return
	}

	// Read the file into a byte slice
	bs = make([]byte, stat.Size())
	_, err = bufio.NewReader(file).Read(bs)
	if err != nil && err != io.EOF {
		fmt.Println(err)
		return
	}
	return
}

func (m *CoreManager) Stop() {
	m.shouldOff <- 1
	m.inst.Close()
	m.inst = nil
}

func SetEnv(key string, val string) {
	os.Setenv(key, val)
}

// TCPPingResult represents the result of a TCP ping operation
type TCPPingResult struct {
	delay      int64  // Delay in milliseconds
	err        string // Error message if any
	serverAddr string // Server address that was pinged
}

// GetDelay returns the delay in milliseconds
func (r *TCPPingResult) GetDelay() int64 {
	return r.delay
}

// GetError returns the error message if any
func (r *TCPPingResult) GetError() string {
	return r.err
}

// GetServerAddr returns the server address that was pinged
func (r *TCPPingResult) GetServerAddr() string {
	return r.serverAddr
}

// TCPPing performs a TCP ping to the specified host and port
func TCPPing(host string, port int, timeout int) *TCPPingResult {
	result := &TCPPingResult{
		serverAddr: fmt.Sprintf("%s:%d", host, port),
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", result.serverAddr, time.Duration(timeout)*time.Millisecond)
	if err != nil {
		result.err = err.Error()
		result.delay = -1
		return result
	}
	defer conn.Close()

	result.delay = time.Since(start).Milliseconds()
	return result
}

// BatchTCPPing performs multiple TCP pings and returns average delay
func BatchTCPPing(host string, port int, count int, timeout int) *TCPPingResult {
	var totalDelay int64
	var successCount int
	var lastError string

	result := &TCPPingResult{
		serverAddr: fmt.Sprintf("%s:%d", host, port),
	}

	for i := 0; i < count; i++ {
		pingResult := TCPPing(host, port, timeout)
		if pingResult.err != "" {
			lastError = pingResult.err
			continue
		}
		totalDelay += pingResult.delay
		successCount++
		// Add small delay between pings
		time.Sleep(100 * time.Millisecond)
	}

	if successCount == 0 {
		result.err = lastError
		result.delay = -1
		return result
	}

	result.delay = totalDelay / int64(successCount)
	return result
}

// MeasureDelay performs an HTTP GET request to measure delay through the V2Ray instance
func MeasureDelay(inst *core.Instance, url string) (int64, error) {
	if inst == nil {
		return -1, fmt.Errorf("core instance nil")
	}

	tr := &http.Transport{
		TLSHandshakeTimeout: 6 * time.Second,
		DisableKeepAlives:   true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dest, err := v2net.ParseDestination(fmt.Sprintf("%s:%s", network, addr))
			if err != nil {
				return nil, err
			}
			return core.Dial(ctx, inst, dest)
		},
	}

	c := &http.Client{
		Transport: tr,
		Timeout:   12 * time.Second,
	}

	if len(url) == 0 {
		url = "https://www.google.com/generate_204"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return -1, fmt.Errorf("status != 20x: %s", resp.Status)
	}

	return time.Since(start).Milliseconds(), nil
}

// MeasureOutboundDelay measures the delay using a temporary V2Ray instance with the given config
//
//export MeasureOutboundDelay
func MeasureOutboundDelay(configContent string, url string) (int64, error) {
	config, err := serial.LoadJSONConfig(strings.NewReader(configContent))
	if err != nil {
		return -1, err
	}

	// Don't listen to anything for test purpose
	config.Inbound = nil
	// Keep only basic features: log, dispatcher, InboundConfig, OutboundConfig
	if len(config.App) > 5 {
		config.App = config.App[:5]
	}

	inst, err := core.New(config)
	if err != nil {
		return -1, err
	}

	err = inst.Start()
	if err != nil {
		return -1, err
	}
	defer inst.Close()

	return MeasureDelay(inst, url)
}

// DelayMeasurementResult represents the result of a delay measurement operation
type DelayMeasurementResult struct {
	delay int64
	err   string
}

// GetDelay returns the delay in milliseconds
func (r *DelayMeasurementResult) GetDelay() int64 {
	return r.delay
}

// GetError returns the error message if any
func (r *DelayMeasurementResult) GetError() string {
	return r.err
}

// MeasureOutboundDelayWithResult wraps MeasureOutboundDelay to return a DelayMeasurementResult
//
//export MeasureOutboundDelayWithResult
func MeasureOutboundDelayWithResult(configContent string, url string) *DelayMeasurementResult {
	result := &DelayMeasurementResult{}

	delay, err := MeasureOutboundDelay(configContent, url)
	if err != nil {
		result.err = err.Error()
		result.delay = -1
		return result
	}

	result.delay = delay
	return result
}
