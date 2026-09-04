// Package operator is Headhunter-Core's operator-lite: it reads a git-declared
// scraper catalog and launches one Kubernetes Job per scraper (scale-to-zero via
// ttlSecondsAfterFinished). Core never scrapes itself — every scraper runs in an
// isolated Job that POSTs RawPostings back to /api/scan/ingest.
package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/store"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ScraperDef is one catalog entry: an image + the env that configures it.
type ScraperDef struct {
	Name  string            `json:"name"`
	Image string            `json:"image"`
	Env   map[string]string `json:"env"`
}

// Catalog is the git-declared scraper set (mounted from a ConfigMap).
type Catalog struct {
	Keywords string       `json:"keywords"`
	Scrapers []ScraperDef `json:"scrapers"`
}

// Operator launches scraper Jobs from the catalog.
type Operator struct {
	cs          *kubernetes.Clientset
	namespace   string
	catalogPath string
	ingestURL   string
	store       *store.Store // for recording/reading scan last-run
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// New builds the operator from in-cluster config; returns an error (operator
// disabled) when not running in a cluster. st is used to persist/read the scan
// last-run (may be nil).
func New(st *store.Store) (*Operator, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Operator{
		cs:          cs,
		namespace:   env("POD_NAMESPACE", "headhunter"),
		catalogPath: env("SCAN_CATALOG", "/etc/headhunter/scan-catalog.json"),
		ingestURL:   env("SELF_INGEST_URL", "http://headhunter-core.headhunter.svc.cluster.local:8080/api/scan/ingest"),
		store:       st,
	}, nil
}

func (o *Operator) loadCatalog() (*Catalog, error) {
	b, err := os.ReadFile(o.catalogPath)
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	return &c, nil
}

// RunCycle launches a Job per catalog scraper. Returns the count launched.
func (o *Operator) RunCycle(ctx context.Context) (int, error) {
	cat, err := o.loadCatalog()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sc := range cat.Scrapers {
		if err := o.launch(ctx, sc, cat.Keywords); err != nil {
			log.Printf("operator: launch %s: %v", sc.Name, err)
			continue
		}
		n++
	}
	log.Printf("operator: cycle launched %d/%d scrapers", n, len(cat.Scrapers))
	if o.store != nil {
		rec, _ := json.Marshal(map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "launched": n})
		if err := o.store.SetConfig(ctx, "scan_last_run", rec); err != nil {
			log.Printf("operator: record last-run: %v", err)
		}
	}
	return n, nil
}

// Status reports the operator's scan state: the persisted last-run plus the live
// state of the current scan Jobs (so the UI/MCP can show it without kubectl).
func (o *Operator) Status(ctx context.Context) (map[string]any, error) {
	out := map[string]any{"running": false, "lastRun": nil, "lastLaunched": 0, "jobs": []any{}}
	if o.store != nil {
		if cfg, err := o.store.GetAllConfig(ctx); err == nil {
			if raw, ok := cfg["scan_last_run"]; ok {
				var lr struct {
					At       string `json:"at"`
					Launched int    `json:"launched"`
				}
				if json.Unmarshal(raw, &lr) == nil {
					out["lastRun"] = lr.At
					out["lastLaunched"] = lr.Launched
				}
			}
		}
	}
	jl, err := o.cs.BatchV1().Jobs(o.namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=headhunter-scan"})
	if err != nil {
		return out, err
	}
	jobs := []any{}
	running := false
	for _, j := range jl.Items {
		state := "pending"
		switch {
		case j.Status.Active > 0:
			state = "running"
			running = true
		case j.Status.Succeeded > 0:
			state = "succeeded"
		case j.Status.Failed > 0:
			state = "failed"
		}
		entry := map[string]any{"ats": j.Labels["ats"], "state": state}
		if j.Status.StartTime != nil {
			entry["startedAt"] = j.Status.StartTime.Time.UTC().Format(time.RFC3339)
		}
		if j.Status.CompletionTime != nil {
			entry["finishedAt"] = j.Status.CompletionTime.Time.UTC().Format(time.RFC3339)
		}
		jobs = append(jobs, entry)
	}
	out["jobs"] = jobs
	out["running"] = running
	return out, nil
}

func (o *Operator) launch(ctx context.Context, sc ScraperDef, keywords string) error {
	envs := []corev1.EnvVar{
		{Name: "CORE_INGEST_URL", Value: o.ingestURL},
		{Name: "ROLE_KEYWORDS", Value: keywords},
	}
	for k, v := range sc.Env {
		envs = append(envs, corev1.EnvVar{Name: k, Value: v})
	}
	var (
		ttl     int32 = 600
		backoff int32 = 2
		nonRoot       = true
		uid     int64 = 65532
		noPriv        = false
		roRoot        = true
	)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "headhunter-scan-" + sc.Name + "-",
			Namespace:    o.namespace,
			Labels:       map[string]string{"app": "headhunter-scan", "ats": sc.Name},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "headhunter-scan", "ats": sc.Name}},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					EnableServiceLinks: &noPriv,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &nonRoot,
						RunAsUser:      &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:  "scraper",
						Image: sc.Image,
						// Clean-pull every cycle: with a moving tag (:latest) this
						// re-checks the registry digest and pulls only when the SHA
						// changed, so a freshly-built scraper rolls out on the next scan.
						ImagePullPolicy: corev1.PullAlways,
						Env:             envs,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("25m"),
								corev1.ResourceMemory: resource.MustParse("32Mi"),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &noPriv,
							ReadOnlyRootFilesystem:   &roRoot,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
	_, err := o.cs.BatchV1().Jobs(o.namespace).Create(ctx, job, metav1.CreateOptions{})
	return err
}

// RunTicker runs a cycle every SCAN_INTERVAL (default 6h). Does not fire on
// startup — the first scheduled scan is one interval after boot.
func (o *Operator) RunTicker(ctx context.Context) {
	interval := 6 * time.Hour
	if d, err := time.ParseDuration(os.Getenv("SCAN_INTERVAL")); err == nil && d > 0 {
		interval = d
	}
	log.Printf("operator: scheduling a scan cycle every %s", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := o.RunCycle(ctx); err != nil {
				log.Printf("operator: scheduled cycle: %v", err)
			}
		}
	}
}
