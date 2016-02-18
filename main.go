package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/asaskevich/govalidator"
)

func validateUrl(u string) (*url.URL, error) {
	// Verify the target is HTTP or HTTPS.
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("%s: must be an http(s) URL.\n", u)
	}

	// Validate the URL.
	if !govalidator.IsURL(u) {
		return nil, fmt.Errorf("%s: must be a valid URL.\n", u)
	}

	// Parse the target as a URL.
	ua, err := url.Parse(u)
	if err != nil {
		panic(err)
	}

	return ua, nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "USAGE: multireq [listen-addr] [target]...")
		os.Exit(1)
	}

	listen := os.Args[1]
	targets := os.Args[2:]
	urls := make([]*url.URL, len(targets))

	failed := false
	for i, v := range targets {
		ua, err := validateUrl(v)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
			continue
		}
		urls[i] = ua
	}
	if failed {
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Allocate a channel for the HTTP responses.
		responses := make(chan *http.Response, len(targets))

		// Allocate a cancellation channel for the targets' HTTP requests.
		cancels := make([]chan struct{}, len(targets))

		// Allocate a failure channel to collect failed HTTP requests.
		fails := make(chan bool, len(targets))

		// Channel to indicate all requests have failed.
		allFailed := make(chan struct{})

		r.RequestURI = ""
		r.URL.Scheme = "http"

		// Create and send out an HTTP request to each target.
		for i := range targets {
			req := *r
			req.URL.Host = urls[i].Host
			req.URL, _ = url.Parse(r.URL.String())
			cancels[i] = make(chan struct{})
			req.Cancel = cancels[i]

			go func() {
				rt := &http.Transport{DisableKeepAlives: true}
				res, err := rt.RoundTrip(&req)

				switch {
				case err != nil:
					log.Printf("request failed: %s\n", err)
					fails <- true
				case res.StatusCode >= 500 || res.StatusCode == 408:
					log.Printf("target (%s) unsatisfying status: %d", req.URL, res.StatusCode)
					fails <- true
				default:
					log.Printf("target (%s) responded with %d", req.URL, res.StatusCode)
					responses <- res
				}
			}()
		}

		// Listen to the requests' failure channel; close it when all requests fail.
		go func() {
			for i := 0; i < len(targets); i++ {
				_, ok := <-fails
				if !ok {
					return
				}
			}

			// Close the channel, signalling that all requests have failed.
			close(allFailed)
		}()

		// Wait for a successful response (or failures across the board).
		for {
			select {
			case res := <-responses:
				// Close the cancel channels.
				for _, c := range cancels {
					close(c)
				}

				// Copy headers over.
				for k, v := range res.Header {
					w.Header()[k] = v
				}
				w.WriteHeader(res.StatusCode)

				written, err := io.Copy(w, res.Body)
				if err != nil {
					log.Printf("io.Copy error: %s", err)
				}

				log.Printf("io.Copy %d bytes written", written)
				return
			case _, ok := <-allFailed:
				if !ok {
					// All requests have met failure.
					w.WriteHeader(503)
					w.(http.Flusher).Flush()
					return
				}
			}
		}
	})

	log.Printf("listening on %s", listen)
	err := http.ListenAndServe(listen, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
