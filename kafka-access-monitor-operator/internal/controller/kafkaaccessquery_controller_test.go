package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	accessmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/api/v1alpha1"
	"github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/internal/discovery"
)

// --- START: Mocks for dependencies ---
// We only need to mock interfaces. Concrete types will be initialized properly.

type mockMetricsFinder struct{}

func (m *mockMetricsFinder) FetchBrokerMetrics(ctx context.Context, brokerPodName string, problemTime time.Time) (*discovery.BrokerMetrics, error) {
	return &discovery.BrokerMetrics{CPUUtilization: 0.5}, nil
}
func (m *mockMetricsFinder) Close() error { return nil }

type mockAIAnalyzer struct{}

func (m *mockAIAnalyzer) Analyze(ctx context.Context, podInfo accessmonitorv1alpha1.PodAccessInfo, problem accessmonitorv1alpha1.ProblemDetail) (string, error) {
	return "Mock AI Analysis", nil
}

// --- END: Mocks ---

var _ = Describe("KafkaAccessQuery Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		kafkaaccessquery := &accessmonitorv1alpha1.KafkaAccessQuery{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind KafkaAccessQuery")
			err := k8sClient.Get(ctx, typeNamespacedName, kafkaaccessquery)
			if err != nil && errors.IsNotFound(err) {
				resource := &accessmonitorv1alpha1.KafkaAccessQuery{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: accessmonitorv1alpha1.KafkaAccessQuerySpec{
						// Fill the spec with test values if necessary
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &accessmonitorv1alpha1.KafkaAccessQuery{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance KafkaAccessQuery")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")

			// --- FIX: Initialize all components with their dependencies ---
			// We use the real constructors, passing the test client.
			// For components that need a clientset, we use a fake one.
			fakeClientset := fake.NewSimpleClientset()

			// We assume the parser constructor doesn't fail with default values.
			// Adjust if your NewDynamicParser needs a real path.
			parser, _ := discovery.NewDynamicParser("", ctrl.Log.WithName("test-parser"))

			controllerReconciler := &KafkaAccessQueryReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				Log:               ctrl.Log.WithName("test-reconciler"),
				PodFinder:         discovery.NewPodFinder(k8sClient),
				ConfigMapFinder:   discovery.NewConfigMapFinder(k8sClient),
				SecretFinder:      discovery.NewSecretFinder(k8sClient),
				AuthnEnricher:     discovery.NewAuthnEnricher(k8sClient),
				DiagnosticsFinder: discovery.NewDiagnosticsFinder(k8sClient, fakeClientset),
				AIAnalyzer:        &mockAIAnalyzer{},
				KafkaParser:       parser,
				SlackReporter:     discovery.NewSlackReporter(""), // Empty webhook URL for test
				BrokerLogFinders:  make(map[string]*discovery.BrokerLogFinder),
				MetricsFinder:     &mockMetricsFinder{},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

		})
	})
})
