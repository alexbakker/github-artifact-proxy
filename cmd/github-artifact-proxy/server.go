package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v44/github"
	"github.com/julienschmidt/httprouter"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
)

const (
	targetLockTimeout = 30 * time.Second
)

type Server struct {
	*ServerConfig
	router *httprouter.Router

	m       sync.Mutex
	clients map[*Target]*github.Client
}

type ServerConfig struct {
	Config         *Config
	BasePath       string
	DownloadDir    string
	GithubCacheTTL time.Duration
}

func NewServer(cfg *ServerConfig) *Server {
	if !strings.HasPrefix(cfg.BasePath, "/") {
		cfg.BasePath = "/" + cfg.BasePath
	}
	s := Server{
		ServerConfig: cfg,
		clients:      make(map[*Target]*github.Client),
	}

	r := httprouter.New()

	fs := s.getFileServer(s.DownloadDir)
	r.GET(s.buildURLPath("/artifacts/*filename"), fs)
	r.GET(s.buildURLPath("/targets/:target/artifacts/:artifact/*filename"), s.handleTargetRequest)

	s.router = r
	return &s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) handleTargetRequest(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	targetId := params.ByName("target")
	artifactName := params.ByName("artifact")
	filename := strings.TrimPrefix(params.ByName("filename"), "/")
	logCtx := log.WithFields(log.Fields{
		"addr":     r.RemoteAddr,
		"path":     r.URL.Path,
		"target":   targetId,
		"artifact": artifactName,
		"filename": filename,
	})
	logCtx.Info("handling request")

	target, ok := s.getTarget(targetId)
	if !ok {
		logCtx.Warn("target not found")
		httpError(w, http.StatusNotFound)
		return
	}

	lockCtx, cancel := context.WithTimeout(r.Context(), targetLockTimeout)
	defer cancel()
	if err := target.Lock(lockCtx); err != nil {
		logCtx.WithError(err).WithField("timeout", targetLockTimeout).Error("unable to acquire target lock")
		httpError(w, http.StatusNotFound)
		return
	}
	defer target.Unlock()

	client := s.getClient(target)

	if time.Since(target.latestArtifactTime) > s.GithubCacheTTL {
		listOpts := github.ListWorkflowRunsOptions{}
		if target.Branch != nil {
			listOpts.Branch = *target.Branch
		}
		if target.Event != nil {
			listOpts.Event = *target.Event
		}
		if target.Status != nil {
			listOpts.Status = *target.Status
		}

		wfRes, ghRes, err := client.Actions.ListWorkflowRunsByFileName(r.Context(), target.Owner, target.Repo, target.Filename, &listOpts)
		if err != nil {
			if ghRes != nil && ghRes.StatusCode == http.StatusNotFound {
				logCtx.WithError(err).Warn("unable to obtain workflow runs")
				httpError(w, http.StatusNotFound)
				return
			}

			logCtx.WithError(err).Error("unable to obtain workflow runs")
			httpError(w, http.StatusInternalServerError)
			return
		}

		logCtx.WithFields(log.Fields{
			"workflow": target.Filename,
			"amount":   len(wfRes.WorkflowRuns),
		}).Info("retrieved workflow runs")

		if len(wfRes.WorkflowRuns) == 0 {
			logCtx.Warn("list of workflow runs is empty")
			httpError(w, http.StatusNotFound)
			return
		}

		// We assume that the first workflow run in the list is the latest one. Luckily this
		// appears to always be the case, because there seems to be no way to specify
		// a sorting preference.
		run := wfRes.WorkflowRuns[0]

		afRes, _, err := client.Actions.ListWorkflowRunArtifacts(r.Context(), target.Owner, target.Repo, *run.ID, nil)
		if err != nil {
			logCtx.WithError(err).Error("unable to obtain artifact list")
			httpError(w, http.StatusInternalServerError)
			return
		}

		logCtx.WithFields(log.Fields{
			"workflow": target.Filename,
			"amount":   len(afRes.Artifacts),
		}).Info("retrieved workflow artifacts")

		var artifact *github.Artifact
		for _, af := range afRes.Artifacts {
			if af.Name != nil && *af.Name == artifactName {
				artifact = af
				break
			}
		}

		if artifact == nil || artifact.ID == nil {
			logCtx.Warn("artifact not found")
			httpError(w, http.StatusNotFound)
			return
		}

		target.latestArtifactTime = time.Now()
		target.latestArtifact = artifact

		logCtx.WithFields(log.Fields{
			"id":         *target.latestArtifact.ID,
			"created_at": target.latestArtifact.CreatedAt,
		}).Info("retrieved latest artifact metadata")
	} else {
		logCtx.WithFields(log.Fields{
			"id":         *target.latestArtifact.ID,
			"created_at": target.latestArtifact.CreatedAt,
		}).Info("using cached artifact metadata")
	}

	dlDir := s.getArtifactCacheDir(*target.latestArtifact.ID)
	dlPath := s.buildURLPath(fmt.Sprintf("/artifacts/%d/%s", *target.latestArtifact.ID, filename))
	if _, err := os.Stat(dlDir); err == nil {
		logCtx.WithFields(log.Fields{
			"id": *target.latestArtifact.ID,
		}).Info("redirecting to cached artifact")

		http.Redirect(w, r, dlPath, http.StatusFound)
		return
	}

	logCtx.WithFields(log.Fields{
		"id": *target.latestArtifact.ID,
	}).Info("downloading artifact")

	url, _, err := client.Actions.DownloadArtifact(r.Context(), target.Owner, target.Repo, *target.latestArtifact.ID, true)
	if err != nil {
		logCtx.WithError(err).Error("unable to obtain artifact download url")
		httpError(w, http.StatusInternalServerError)
		return
	}

	res, err := client.Client().Get(url.String())
	if err != nil {
		logCtx.WithError(err).Error("unable to prepare artifact download http request")
		httpError(w, http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()

	tempZipFile, err := os.CreateTemp(os.TempDir(), fmt.Sprintf("gh-artifact-%d-*.zip", *target.latestArtifact.ID))
	if err != nil {
		logCtx.WithError(err).Error("unable to create temporary file to download the artifact zip to")
		httpError(w, http.StatusInternalServerError)
		return
	}
	defer deleteFile(logCtx, tempZipFile.Name())

	if _, err := io.Copy(tempZipFile, res.Body); err != nil {
		logCtx.WithError(err).Error("unable to download artifact zip")
		httpError(w, http.StatusInternalServerError)
		return
	}

	zipReader, err := zip.OpenReader(tempZipFile.Name())
	if err != nil {
		logCtx.WithError(err).Error("unable to open zip file")
		httpError(w, http.StatusInternalServerError)
		return
	}
	defer zipReader.Close()

	// Before extracting the ZIP file, check if the requested file actually
	// exists.
	file, err := zipReader.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			logCtx.WithError(err).Warn("unable to open file inside zip")
			httpError(w, http.StatusNotFound)
		} else {
			logCtx.WithError(err).Error("unable to open file inside zip")
			httpError(w, http.StatusInternalServerError)
		}
		return
	}
	file.Close()

	if err := os.MkdirAll(dlDir, os.ModePerm); err != nil {
		logCtx.WithError(err).Error("unable to create directory to unzip the artifact to")
		httpError(w, http.StatusInternalServerError)
		return
	}

	if err := Unzip(zipReader, dlDir); err != nil {
		logCtx.WithError(err).Error("unable to unzip artifact")
		httpError(w, http.StatusInternalServerError)

		deleteDir(logCtx, dlDir)
		return
	}

	w.Header().Add("Cache-Control", "no-cache")
	http.Redirect(w, r, dlPath, http.StatusFound)
}

func (s *Server) getClient(t *Target) *github.Client {
	s.m.Lock()
	defer s.m.Unlock()

	ghClient, ok := s.clients[t]
	if !ok {
		var client *http.Client
		if t.Token != nil {
			client = oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(
				&oauth2.Token{AccessToken: s.Config.Tokens[*t.Token]},
			))
		} else {
			client = new(http.Client)
		}

		client.Timeout = 30 * time.Second

		ghClient = github.NewClient(client)
		s.clients[t] = ghClient
	}

	return ghClient
}

func (s *Server) getTarget(name string) (*Target, bool) {
	s.m.Lock()
	defer s.m.Unlock()

	target, ok := s.Config.Targets[name]
	return target, ok
}

func (s *Server) getArtifactCacheDir(artifactID int64) string {
	return filepath.Join(s.DownloadDir, "artifacts", strconv.FormatInt(artifactID, 10))
}

func (s *Server) buildURLPath(part string) string {
	return path.Join(s.BasePath, part)
}

func (s *Server) getFileServer(dir string) httprouter.Handle {
	fs := http.StripPrefix(s.BasePath, http.FileServer(http.Dir(dir)))
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		fs.ServeHTTP(w, r)
	}
}

func deleteFile(logCtx *log.Entry, filename string) {
	if err := os.Remove(filename); err != nil {
		log.WithField("file", filename).Error("unable to delete file")
	}
}

func deleteDir(logCtx *log.Entry, dir string) {
	if err := os.RemoveAll(dir); err != nil {
		log.WithField("dir", dir).Error("unable to delete file")
	}
}

func httpError(w http.ResponseWriter, status int) {
	msg := fmt.Sprintf("%d %s", status, strings.ToLower(http.StatusText(status)))
	http.Error(w, msg, status)
}
