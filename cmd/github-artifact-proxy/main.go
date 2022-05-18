package main

import (
	"flag"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	downloadDir  string
	httpAddr     string
	httpBasePath string
	configFile   string
	ghCacheTTL   time.Duration
)

func main() {
	flag.StringVar(&downloadDir, "download-dir", "", "the directory to download artifacts to (required)")
	flag.DurationVar(&ghCacheTTL, "github-api-cache-ttl", 5*time.Minute, "the duration after which cached GitHub API responses are invalidated")
	flag.StringVar(&httpAddr, "http-addr", "", "the adddress the HTTP server should listen on (required)")
	flag.StringVar(&httpBasePath, "http-base-path", "/", "the base path prefixed to all URL paths")
	flag.StringVar(&configFile, "config", "", "the filename of the configuration file (required)")
	flag.Parse()

	if downloadDir == "" {
		log.Fatal("flag -download-dir is required")
	}
	if httpAddr == "" {
		log.Fatal("flag -http-addr is required")
	}
	if configFile == "" {
		log.Fatal("flag -config is required")
	}

	cfg, err := LoadConfig(configFile)
	if err != nil {
		log.WithError(err).Fatal("unable to read config file")
	}

	log.WithField("addr", httpAddr).Info("starting http server")

	server := NewServer(&ServerConfig{
		Config:         cfg,
		BasePath:       httpBasePath,
		DownloadDir:    downloadDir,
		GithubCacheTTL: ghCacheTTL,
	})
	http.ListenAndServe(httpAddr, server)
}
