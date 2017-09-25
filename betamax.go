package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"net"
	"net/http"
	"net/url"
	"os"

	"./proxy"
)

func main() {
	cassetteDirectory := flag.String("cassete-directory", "./cassettes", "directory when recorded interactions are written")
	port := flag.Int("port", 8080, "port for proxy to listen on")
	target := flag.String("target-url", "", "remote target url to proxy requests to")

	flag.Parse()

	if *target == "" {
		fmt.Println("No target url given.")
		flag.Usage()
		os.Exit(1)
	}

	targetURL, err := url.ParseRequestURI(*target)
	if err != nil {
		fmt.Printf("Target-url is invalid. Try http://%s\n", *target)
		os.Exit(1)
	}

	if targetURL.Scheme != "http" && targetURL.Scheme != "https" {
		fmt.Println("Target-url must have schema (http, https)")
		os.Exit(1)
	}

	server := setup(*port, targetURL, *cassetteDirectory)

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	go func() {
		log.Printf("Betamax server proxy to %v listening on %s\n", targetURL, server.Addr)
		log.Printf("Note: there is no cassette in the tray\n")

		if err := server.Serve(listener); err != nil {
			log.Fatal(err)
		}
	}()

	graceful(server, 5*time.Second)
}

func setup(port int, targetURL *url.URL, cassetteDirectory string) *http.Server {
	timeout := time.Second * 15

	addr := fmt.Sprintf("0.0.0.0:%d", port)

	sourceURL, _ := url.ParseRequestURI("http://" + addr)

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
		IdleTimeout:  timeout,
		Handler:      proxy.Proxy(sourceURL, targetURL, cassetteDirectory),
	}

	server.RegisterOnShutdown(func() {
		log.Println("\nThank you for using BetaMax!")
	})

	return server
}

func graceful(server *http.Server, timeout time.Duration) {
	stopCh := make(chan os.Signal, 1)

	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	<-stopCh

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Printf("\nShutdown with timeout: %s\n", timeout)

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Error: %v\n", err)
	} else {
		log.Println("Server stopped")
	}
}
