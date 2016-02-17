package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"reflect"
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
			fmt.Println(err.Error())
			failed = true
			continue
		}
		urls[i] = ua
	}
	if failed {
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Allocate channels for each target's HTTP request.
		channels := make([]chan *http.Response, len(targets))
		for i := range channels {
			channels[i] = make(chan *http.Response)
		}

		// Allocate a cancellation channel for each target's HTTP request.
		cancels := make([]chan struct{}, len(targets))

		// Allocate a failure channel for each target's HTTP request.
		fails := make([]chan struct{}, len(targets))

		r.RequestURI = ""
		r.URL.Scheme = "http"

		// Create and send out an HTTP request to each target.
		for i := range targets {
			req := *r
			req.URL.Host = urls[i].Host
			req.URL, _ = url.Parse(r.URL.String())
			cancels[i] = make(chan struct{})
			fails[i] = make(chan struct{})
			req.Cancel = cancels[i]
			fail := fails[i]

			channel := channels[i]

			go func() {
				rt := &http.Transport{DisableKeepAlives: true}
				res, err := rt.RoundTrip(&req)
				if err != nil {
					log.Printf("request failed: %s\n", err)
					close(fail)
				} else if res.StatusCode >= 500 || res.StatusCode == 408 {
					log.Printf("target (%s) unsatisfying status: %d", req.URL, res.StatusCode)
					close(fail)
				} else {
					log.Printf("target (%s) responded with %d", req.URL, res.StatusCode)
					channel <- res
				}
			}()
		}

		// Listen to all requests' failure channels; issue a signal when all requests fail.
		failed := make(chan struct{})
		go func() {
			for i := range fails {
				<-fails[i]
			}
			close(failed)
		}()

		// Listen to the request channels for a response.
		cases := make([]reflect.SelectCase, len(channels)+1)
		for i, ch := range channels {
			cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}
		}
		// Plus an extra channel for the failure case.
		cases[len(channels)] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(failed)}

		// Wait for a successful response (or failures across the board).
		for {
			chosen, value, _ := reflect.Select(cases)
			// Handle the case where all requests have met failure.
			if value.Kind() == reflect.Struct {
				w.WriteHeader(503)
				break
			}

			res := value.Interface().(*http.Response)

			// Close all of the other pending request channels.
			for i := range cancels {
				if i != chosen {
					close(cancels[i])
				}
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
			break
		}
	})

	log.Printf("listening on %s", listen)
	err := http.ListenAndServe(listen, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
