// +build e2e

/*
Copyright 2019 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"encoding/json"
	"testing"
	// adding testify: https://github.com/stretchr/testify
	//"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	//"github.com/stretchr/testify/mock"
	//"github.com/stretchr/testify/suite"
	// --
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"knative.dev/pkg/test/logstream"
	v1a1test "knative.dev/serving/test/v1alpha1"

	v1 "knative.dev/serving/pkg/apis/serving/v1"
	"knative.dev/serving/test"
)


type MigrationTestSuite struct {
	suite.Suite
	names test.ResourceNames
	cancel logstream.Canceler
	clients *test.Clients
}

func (s *MigrationTestSuite) SetupSuite() {
	s.names = test.ResourceNames {
		Service: test.ObjectNameForTest(t),
		Image:   "helloworld",
	}
	s.clients = test.Setup(s.T())
	test.CleanupOnInterrupt(func() { test.TearDown(s.clients, s.names) })
}

func (s *MigrationTestSuite) TearDownSuite() {
	test.TearDown(s.clients, s.names)
}

func (s *MigrationTestSuite) SetupTest() {
	s.T().Parallel()
	s.cancel = logstream.Start(s.T())
}

func (s *MigrationTestSuite) TearDownTest() {
	cancel := s.cancel
	cancel()
}
/*
func (s *MigrationTestSuite) BeforeTest(_, _ string) {
	
}

func (s *MigrationTestSuite) AfterTest(_, _ string) {
	
}
*/

func (suite *MigrationTestSuite)TestTranslation() {
	require := require.New(suite.T())

	suite.T().Log("Creating a new Service")
	// Create a legacy RunLatest service.  This should perform conversion during the webhook
	// and return back a converted service resource.
	service, err := v1a1test.CreateLatestServiceLegacy(suite.T(), clients, names)
	require.NotNil(err, "Failed to create initial Service: %v: %v", names.Service, err)

	// Access the service over the v1 endpoint.
	v1b1, err := clients.ServingClient.Services.Get(service.Name, metav1.GetOptions{})
	require.NotNil(err, "Failed to get v1.Service: %v: %v", names.Service, err)

	// Access the service over the v1 endpoint.
	v1, err := clients.ServingClient.Services.Get(service.Name, metav1.GetOptions{})
	require.NotNil(err, "Failed to get v1.Service: %v: %v", names.Service, err)

	// Check that all PodSpecs match
	require.True(equality.Semantic.DeepEqual(v1b1.Spec.Template.Spec.PodSpec, service.Spec.Template.Spec.PodSpec),
		"Failed to parse unstructured as v1.Service: %v: %v", names.Service, err)
	require.True(equality.Semantic.DeepEqual(v1.Spec.Template.Spec.PodSpec, service.Spec.Template.Spec.PodSpec),
		"Failed to parse unstructured as v1.Service: %v: %v", names.Service, err)
}

func (suite *MigrationTestSuite)TestV1beta1Rejection() {
	require := require.New(suite.T())

	suite.T().Log("Creating a new Service")
	// Create a legacy RunLatest service, but give it the TypeMeta of v1.
	service := v1a1test.LatestServiceLegacy(names)
	service.APIVersion = v1.SchemeGroupVersion.String()
	service.Kind = "Service"

	// Turn it into an unstructured resource for sending through the dynamic client.
	b, err := json.Marshal(service)
	require.Nil(err, "Failed to marshal v1alpha1.Service: %v: %v", names.Service, err)
	u := &unstructured.Unstructured{}
	err1 := json.Unmarshal(b, u)
	require.NotNil(err1, "Failed to unmarshal as unstructured: %v: %v", names.Service, err1)

	// Try to create the "run latest" service through v1.
	gvr := v1.SchemeGroupVersion.WithResource("services")
	svc, err2 := clients.Dynamic.Resource(gvr).Namespace(service.Namespace).
		Create(u, metav1.CreateOptions{})
	require.NotNil(err2, "Unexpected success creating %#v", svc)
}

func TestMigrationTestSuite(t *testing.T) {
	suite.Run(t, new(MigrationTestSuite))
}