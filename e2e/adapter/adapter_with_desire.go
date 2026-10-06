package adapter

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega" //nolint:staticcheck // Gomega matchers are designed to be used with dot import
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client"
	k8sclient "github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client/kubernetes"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/helper"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/labels"
)

// desireConfigMapName is the ConfigMap the example writes into "<cluster id>-remote".
const desireConfigMapName = "cluster-config"

// desireReleases are the Helm releases (app.kubernetes.io/instance) that make up the
// desire delivery stack in the run namespace. adapter is the only adapter a desire
// run deploys: the adapter repo's charts/examples/remote-two-resources task routed
// to a remote transport.
func desireReleases(adapter string) []string {
	return []string{
		"hyperfleet-api",
		"hyperfleet-gateway",
		"clusters",  // sentinel
		"nodepools", // sentinel
		adapter,
		"redis",
		"hyperfleet-applier",
	}
}

// maestroPodPattern matches pods that would put Maestro, its MQTT broker or an OCM
// work agent into the delivery path.
var maestroPodPattern = regexp.MustCompile(`maestro|mqtt|mosquitto|klusterlet|work-agent`) //nolint:misspell // broker name

// desireRemoteNamespace is the Namespace the example creates for a cluster.
func desireRemoteNamespace(clusterID string) string {
	return clusterID + "-remote"
}

// desireDeploymentReady reports whether every desired replica of d is updated and
// ready and the Deployment is Available, the same test as kubectl rollout status,
// except that at least one replica is required: a Deployment scaled to 0 reports
// Available but serves nothing.
func desireDeploymentReady(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = max(*d.Spec.Replicas, 1)
	}
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedReplicas >= want &&
		d.Status.ReadyReplicas >= want &&
		k8sclient.HasDeploymentCondition(d, appsv1.DeploymentAvailable, corev1.ConditionTrue)
}

// No CI job runs this suite. Run it by hand on a local kind stack with desire delivery
// (docs/setup.md, Option 3) and --label-filter=desire-transport. It is temporary: once
// HYPERFLEET-1503 switches the stack to the applier, HYPERFLEET-1505 removes it and the
// tier suites become the proof that delivery works without Maestro.
var _ = ginkgo.Describe("[Suite: adapter][desire-transport] Adapter Framework - Desire Transport without Maestro",
	ginkgo.Ordered,
	// No severity label: the tier jobs deploy no applier, and a tier label would select this suite there.
	ginkgo.Label(labels.DesireTransport),
	func() {
		var (
			h          *helper.Helper
			adapter    string // h.Cfg.Desire.Adapter
			clusterID  string
			generation int32
		)

		ginkgo.BeforeAll(func() {
			h = helper.New()
			// The run namespace holds the whole stack; empty would mean "all namespaces".
			Expect(h.Cfg.Namespace).NotTo(BeEmpty(), "NAMESPACE must name the run namespace")
			// The only adapter a desire run deploys. The API requires just this adapter
			// there, so h.Cfg.Adapters (the Maestro set) does not apply.
			adapter = h.Cfg.Desire.Adapter

			// Registered here rather than in the create spec: in an Ordered container a
			// DeferCleanup made inside an It runs when that It ends, which would delete the
			// cluster before the delete spec. From BeforeAll it runs after the last spec,
			// and finds the cluster already gone (404) when the delete spec passed.
			ginkgo.DeferCleanup(func(ctx context.Context) {
				if clusterID == "" {
					return
				}
				if err := h.CleanupTestCluster(ctx, clusterID); err != nil {
					ginkgo.GinkgoWriter.Printf("Warning: failed to cleanup cluster %s: %v\n", clusterID, err)
				}
			})
		})

		// Fails when the stack was brought up without desire delivery, or when Maestro
		// leaks into the run's delivery path: its pods or the adapter's effective config.
		ginkgo.It("should run the desire delivery stack with no Maestro component in the delivery path",
			func(ctx context.Context) {
				ns := h.Cfg.Namespace

				ginkgo.By("verifying the API, gateway, sentinels, desire adapter, redis and applier Deployments are Ready")
				Eventually(func(g Gomega) {
					for _, release := range desireReleases(adapter) {
						deployments, err := h.K8sClient.FetchDeploymentsByLabels(ctx, ns,
							map[string]string{"app.kubernetes.io/instance": release})
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(deployments).NotTo(BeEmpty(),
							"release %s has no Deployment in namespace %s; was the stack brought up with DESIRE_DELIVERY_ENABLED=true?",
							release, ns)
						for i := range deployments {
							d := &deployments[i]
							g.Expect(desireDeploymentReady(d)).To(BeTrue(),
								"Deployment %s/%s (release %s) is not Ready: ready=%d updated=%d available=%d",
								ns, d.Name, release, d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas)
						}
					}
				}, h.Cfg.Timeouts.Adapter.Processing, h.Cfg.Polling.Interval).Should(Succeed())

				ginkgo.By("verifying no Maestro, MQTT or OCM agent pod runs in the run namespace")
				pods, err := h.K8sClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
				Expect(err).NotTo(HaveOccurred(), "failed to list pods in namespace %s", ns)
				var maestroPods []string
				for _, pod := range pods.Items {
					// The applier's name embeds the namespace (hyperfleet-applier-<namespace>),
					// so match on the rest of the pod name only.
					if maestroPodPattern.MatchString(strings.ReplaceAll(pod.Name, ns, "")) {
						maestroPods = append(maestroPods, pod.Name)
					}
				}
				Expect(maestroPods).To(BeEmpty(), "pods matching %q in namespace %s", maestroPodPattern, ns)

				ginkgo.By("verifying the desire adapter's config has no Maestro client and only remote transports")
				cm, err := h.K8sClient.GetUniqueConfigMapByLabels(ctx, ns, map[string]string{
					"app.kubernetes.io/instance":  adapter,
					"app.kubernetes.io/component": "adapter-config",
				})
				Expect(err).NotTo(HaveOccurred(), "failed to find the adapter-config ConfigMap of %s", adapter)
				rendered, ok := cm.Data["adapter-config.yaml"]
				Expect(ok).To(BeTrue(), "ConfigMap %s/%s has no adapter-config.yaml key", ns, cm.Name)
				var adapterConfig struct {
					Clients    map[string]any `yaml:"clients"`
					Transports map[string]struct {
						Type string `yaml:"type"`
					} `yaml:"transports"`
				}
				Expect(yaml.Unmarshal([]byte(rendered), &adapterConfig)).To(Succeed(),
					"failed to parse adapter-config.yaml of %s", adapter)
				Expect(adapterConfig.Clients).NotTo(HaveKey("maestro"),
					"%s must not configure a Maestro client", adapter)
				Expect(adapterConfig.Transports).NotTo(BeEmpty(), "%s declares no transports", adapter)
				for name, transport := range adapterConfig.Transports {
					Expect(transport.Type).To(Equal("remote"), "transport %q of %s", name, adapter)
				}
			})

		// Fails when delivery through the applier, or the status it reports back, breaks.
		// The adapter has no Kubernetes access of its own, so the objects on the cluster were
		// created by the applier, and its Applied/Available/Health conditions are built from
		// the live objects the applier reports.
		ginkgo.It("should create a cluster's resources through the applier and report their status",
			func(ctx context.Context) {
				ginkgo.By("creating a cluster")
				cluster, err := h.Client.CreateClusterFromPayload(ctx, h.TestDataPath("payloads/clusters/cluster-request.json"))
				Expect(err).NotTo(HaveOccurred(), "failed to create cluster")
				Expect(cluster.Id).NotTo(BeNil(), "cluster ID should be generated")
				clusterID = *cluster.Id // deleted by the cleanup registered in BeforeAll
				generation = cluster.Generation
				Expect(generation).To(Equal(int32(1)), "a new cluster should have generation 1")
				ginkgo.GinkgoWriter.Printf("Created cluster ID: %s, Name: %s\n", clusterID, cluster.Name)

				ginkgo.By("waiting for the cluster to become Reconciled")
				Eventually(h.PollCluster(ctx, clusterID), h.Cfg.Timeouts.Cluster.Reconciled, h.Cfg.Polling.Interval).
					Should(helper.HaveResourceCondition(client.ConditionTypeReconciled, client.ResourceConditionStatusTrue))

				ginkgo.By("verifying the desire adapter reports Applied, Available and Health True at the cluster generation")
				Eventually(h.PollClusterAdapterStatuses(ctx, clusterID), h.Cfg.Timeouts.Adapter.Processing, h.Cfg.Polling.Interval).
					Should(helper.HaveAllAdaptersAtGeneration([]string{adapter}, generation))

				ginkgo.By("verifying the applier created both objects")
				wantGeneration := strconv.Itoa(int(generation))
				remote := desireRemoteNamespace(clusterID)
				namespace, err := h.GetNamespace(ctx, remote)
				Expect(err).NotTo(HaveOccurred())
				Expect(namespace.Annotations).To(HaveKeyWithValue(client.KeyGeneration, wantGeneration))
				configMap, err := h.GetConfigMap(ctx, remote, desireConfigMapName)
				Expect(err).NotTo(HaveOccurred())
				Expect(configMap.Annotations).To(HaveKeyWithValue(client.KeyGeneration, wantGeneration))
				Expect(configMap.Data).To(HaveKeyWithValue("cluster_id", clusterID))
			})

		// The adapter must see the applier confirm the delete before it reports Finalized,
		// and the API hard-deletes the cluster as soon as it does. So the check after the 404
		// is one-shot: a Namespace still there means the adapter finalized early, a product
		// finding, not a reason to retry.
		ginkgo.It("should delete the cluster's resources through the applier before the cluster is finalized",
			func(ctx context.Context) {
				ginkgo.By("soft-deleting the cluster")
				deleted, err := h.Client.DeleteCluster(ctx, clusterID)
				Expect(err).NotTo(HaveOccurred(), "DELETE request should succeed")
				Expect(deleted.DeletedTime).NotTo(BeNil(), "soft-deleted cluster should have deleted_time set")

				ginkgo.By("waiting for the desire adapter to finalize and the cluster to be hard-deleted")
				// Hard-delete runs inside the status POST that computes Reconciled=True from
				// the required adapters' Finalized=True, so the 404 proves the adapter
				// finalized; Finalized=True itself may never be observable.
				Eventually(h.PollClusterHTTPStatus(ctx, clusterID), h.Cfg.Timeouts.Cluster.Deleted, h.Cfg.Polling.Interval).
					Should(Equal(http.StatusNotFound))

				ginkgo.By("checking once, without retry, that the remote Namespace is gone")
				remote := desireRemoteNamespace(clusterID)
				if namespace, err := h.K8sClient.CoreV1().Namespaces().Get(ctx, remote, metav1.GetOptions{}); err == nil {
					ginkgo.Fail(fmt.Sprintf("Namespace %s still exists (phase %s) after the cluster was hard-deleted",
						remote, namespace.Status.Phase))
				} else {
					Expect(apierrors.IsNotFound(err)).To(BeTrue(), "Get Namespace %s: want NotFound, got %v", remote, err)
				}
			})
	},
)
