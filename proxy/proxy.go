package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func handleConfigRequest(resp http.ResponseWriter, req *http.Request, config *Config) {
	if req.Method == "GET" {
		json.NewEncoder(resp).Encode(config)
		return
	}

	if req.Method == "POST" {
		json.NewDecoder(req.Body).Decode(config)
		err := config.Load()
		if err != nil {
			panic(fmt.Errorf("%s for %s %s", err, req.Method, req.RequestURI))
		}
		return
	}

	if req.Method == "DELETE" {
		err := config.Reset()
		if err != nil {
			panic(fmt.Errorf("%s for %s %s", err, req.Method, req.RequestURI))
		}
		return
	}

	resp.WriteHeader(405)
	fmt.Fprintf(resp, "BetaMax: method %s is not allowed.\n", req.Method)
}

func configHandler(handler http.Handler, config *Config) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/__betamax__/config" {
			handleConfigRequest(resp, req, config)
		} else {
			handler.ServeHTTP(resp, req)
		}
	})
}

func cassetteHandler(handler http.Handler, config *Config) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		msg := ""

		defer func(start time.Time) {
			log.Printf("%s: [%s]\n", msg, time.Now().UTC().Sub(start))
		}(time.Now().UTC())

		if config.Cassette == "" {
			msg = fmt.Sprintf("passthrough: %s %s", req.Method, req.URL)
			handler.ServeHTTP(resp, req)
			return
		}

		if episode := findEpisode(req, config); episode != nil {
			msg = fmt.Sprintf("%s > replaying: %s %s", config.Cassette, req.Method, req.URL)
			serveEpisode(episode, resp)
			return
		}

		if config.RecordNewEpisodes {
			msg = fmt.Sprintf("%s > recording: %s %s", config.Cassette, req.Method, req.URL)
			serveAndRecord(resp, req, handler, config)
			return
		}

		if config.DenyUnrecordedRequests {
			msg = fmt.Sprintf("%s > missed: %s %s", config.Cassette, req.Method, req.URL)
			resp.WriteHeader(499)
			fmt.Fprintf(resp, "BetaMax: request not recorded, neither requested.\n")
			return
		}

		msg = fmt.Sprintf("%s > passthrough: %s %s", config.Cassette, req.Method, req.URL)
		handler.ServeHTTP(resp, req)
	})
}

func rewriteHeaderHandler(handler http.Handler, config *Config) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		if config.RewriteHostHeader {
			req.Host = config.TargetHost
		}

		handler.ServeHTTP(resp, req)
	})
}

func recoverHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Error: %s\n", err)
				http.Error(w, err.(error).Error(), 500)
			}
		}()

		h.ServeHTTP(w, r)
	})
}

// reads all bytes of the request into memory
// returns the read bytes and replaces the request's Reader
// with a refilled reader. seems like there should be a better
// way to do this.
func peekBytes(req *http.Request) (body []byte, err error) {
	body, err = ioutil.ReadAll(req.Body)
	req.Body = ioutil.NopCloser(bytes.NewReader(body))
	return
}

func peekForm(req *http.Request) (form url.Values, err error) {
	body, err := ioutil.ReadAll(req.Body)
	req.Body = ioutil.NopCloser(bytes.NewReader(body))

	err = req.ParseMultipartForm(10000)
	form = req.Form
	req.Body = ioutil.NopCloser(bytes.NewReader(body))
	return
}

func sameURL(a *url.URL, b *url.URL) bool {
	return a.Path == b.Path && a.RawQuery == b.RawQuery && a.Fragment == b.Fragment
}

func sameHeaders(recorded http.Header, newRequest http.Header, config Config) bool {
	for _, header := range config.MatchHeaders {
		for i, _ := range newRequest[header] {
			if len(newRequest[header]) != len(recorded[header]) {
				return false
			}

			if newRequest[header][i] != recorded[header][i] {
				return false
			}
		}
	}
	return true
}

func sameRequest(a *RecordedRequest, b *http.Request, config Config) bool {
	if a.Method != b.Method {
		return false
	}

	if !sameURL(a.URL, b.URL) {
		return false
	}

	if !sameHeaders(a.Header, b.Header, config) {
		return false
	}

	form, _ := peekForm(b)

	for key, _ := range form {
		if len(a.Form[key]) != len(form[key]) {
			return false
		}

		for i, _ := range form[key] {
			if a.Form[key][i] != form[key][i] {
				return false
			}
		}
	}

	if len(form) == 0 {
		body, _ := peekBytes(b)
		if bytes.Compare(a.Body, body) != 0 {
			return false
		}
	}

	return true
}

func serveAndRecord(resp http.ResponseWriter, req *http.Request, handler http.Handler, config *Config) {
	proxyWriter := ProxyResponseWriter{Writer: resp}
	recordedRequest := recordRequest(req)

	handler.ServeHTTP(&proxyWriter, req)
	writeEpisode(Episode{Request: recordedRequest, Response: proxyWriter.Response}, config)
}

func recordRequest(req *http.Request) RecordedRequest {
	body, _ := peekBytes(req)
	form, _ := peekForm(req)
	return RecordedRequest{
		URL:    req.URL,
		Header: req.Header,
		Method: req.Method,
		Body:   body,
		Form:   form,
	}
}

func writeEpisode(episode Episode, config *Config) {
	config.Episodes = append(config.Episodes, episode)
	config.Save()
}

func findEpisode(req *http.Request, config *Config) *Episode {
	for _, episode := range config.Episodes {
		if sameRequest(&episode.Request, req, *config) {
			return &episode
		}
	}
	return nil
}

func serveEpisode(episode *Episode, resp http.ResponseWriter) {
	for k, values := range episode.Response.Header {
		for _, value := range values {
			resp.Header().Add(k, value)
		}
	}
	resp.WriteHeader(episode.Response.StatusCode)
	resp.Write(episode.Response.Body)
}

func Proxy(source *url.URL, target *url.URL, cassetteDir string) http.Handler {
	config := &Config{CassetteDir: cassetteDir, RecordNewEpisodes: true, RewriteHostHeader: true, TargetHost: target.Host}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) (err error) {
		/*
			b, err := ioutil.ReadAll(resp.Body) //Read html
			if err != nil {
				return err
			}
			err = resp.Body.Close()
			if err != nil {
				return err
			}
			b = bytes.Replace(b, []byte(target.Host), []byte(source.Host), -1) // replace html
			body := ioutil.NopCloser(bytes.NewReader(b))
			resp.Body = body
			resp.ContentLength = int64(len(b))
			resp.Header.Set("Content-Length", strconv.Itoa(len(b)))

			if location := resp.Header.Get("Location"); location != "" {
				resp.Header.Set("Location", strings.Replace(location, target.Host, source.Host, -1))
			}
		*/
		return nil
	}

	cassetteHandler := cassetteHandler(proxy, config)
	rewriteHeaderHandler := rewriteHeaderHandler(cassetteHandler, config)
	configHandler := configHandler(rewriteHeaderHandler, config)
	return recoverHandler(configHandler)
}
