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
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/apis"
	pkgTest "knative.dev/pkg/test"
	"knative.dev/pkg/test/logging"
	v1 "knative.dev/serving/pkg/apis/serving/v1"
	serviceresourcenames "knative.dev/serving/pkg/reconciler/service/resources/names"
	"knative.dev/serving/test"
	v1test "knative.dev/serving/test/v1"

	rtesting "knative.dev/serving/pkg/testing/v1"
)

const (
	containerMissing = "ContainerMissing"
)

// TestContainerErrorMsg is to validate the error condition defined at
// https://github.com/knative/serving/blob/master/docs/spec/errors.md
// for the container image missing scenario.
func TestContainerErrorMsg(legacy *testing.T) {
	t := logging.NewTLogger(legacy)
	defer t.CleanUp()
	if strings.HasSuffix(strings.Split(pkgTest.Flags.DockerRepo, "/")[0], ".local") {
		t.V(0).Info("Skipping for local docker repo")
		t.SkipNow()
	}
	t.Parallel()
	clients := test.Setup(t)
	e2eErrors := make([]error, 0)

	names := test.ResourceNames{
		Service: test.ObjectNameForTest(t),
		Image:   test.InvalidHelloWorld,
	}

	defer test.TearDown(clients, names)
	test.CleanupOnInterrupt(func() { test.TearDown(clients, names) })

	// Specify an invalid image path
	// A valid DockerRepo is still needed, otherwise will get UNAUTHORIZED instead of container missing error
	t.V(2).Info("Creating a new Service", "service", names.Service)
	svc, err := createService(legacy, clients, names, 2)
	t.FatalIfErr(err, "Failed to create Service")

	names.Config = serviceresourcenames.Configuration(svc)
	names.Route = serviceresourcenames.Route(svc)

	manifestUnknown := string(transport.ManifestUnknownErrorCode)

	t.Run("API", func(t *logging.TLogger) {
		t.V(1).Info("When the imagepath is invalid, the Configuration should have error status.")
		t.V(8).Info("Wait for ServiceState becomes NotReady. It also waits for the creation of Configuration.")
		err = v1test.WaitForServiceState(clients.ServingClient, names.Service, v1test.IsServiceNotReady, "ServiceIsNotReady")
		t.FatalIfErr(err, "The Service was unexpected state",
			"service", names.Service)

		t.V(8).Info("Checking for 'Container image not present in repository' scenario defined in error condition spec.")
		err = v1test.WaitForConfigurationState(clients.ServingClient, names.Config, func(r *v1.Configuration) (bool, error) {
			cond := r.Status.GetCondition(v1.ConfigurationConditionReady)
			errCtx := [4]interface{}{"configuration", names.Config, "condition", cond}
			ValidateCondition(t.WithValues(errCtx...), cond)
			if cond != nil && !cond.IsUnknown() {
				if cond.IsFalse() && cond.Reason == containerMissing {
					// Spec does not have constraints on the Message
					if !strings.Contains(cond.Message, manifestUnknown) {
						e2eErrors = append(e2eErrors, logging.Error("Bad Condition.Message testing 'Container image not present' scenario",
							"wantMessage", manifestUnknown, errCtx...))
					}
					if cond.Message != "" {
						return true, nil
					}
				}
				return true, logging.Error("The configuration was not marked with expected error condition",
					"wantReason", containerMissing, "wantMessage", "!\"\"", "wantStatus", "False", errCtx...)
			}
			return false, nil
		}, "ContainerImageNotPresent")

		t.FatalIfErr(err, "Failed to validate configuration state")

		revisionName, err := getRevisionFromConfiguration(clients, names.Config)
		t.FatalIfErr(err, "Failed to get revision from configuration", "configuration", names.Config)

		t.V(1).Info("When the imagepath is invalid, the revision should have error status.")
		err = v1test.WaitForRevisionState(clients.ServingClient, revisionName, func(r *v1.Revision) (bool, error) {
			cond := r.Status.GetCondition(v1.RevisionConditionReady)
			errCtx := [4]interface{}{"revision", revisionName, "condition", cond}
			ValidateCondition(t.WithValues(errCtx...), cond)
			if cond != nil {
				if cond.Reason == containerMissing {
					// Spec does not have constraints on the Message
					if !strings.Contains(cond.Message, manifestUnknown) {
						e2eErrors = append(e2eErrors, logging.Error("Bad Condition.Message testing revision with invalid imagepath",
							"wantMessage", manifestUnknown, errCtx...))
					}
					if cond.Message != "" {
						return true, nil
					}
				}
				return true, logging.Error("The revision was not marked with expected error condition",
					"wantReason", containerMissing, "wantMessage", "!\"\"", errCtx...)
			}
			return false, nil
		}, "ImagePathInvalid")

		t.FatalIfErr(err, "Failed to validate revision state")

		t.V(1).Info("Checking to ensure Route is in desired state")
		err = v1test.CheckRouteState(clients.ServingClient, names.Route, v1test.IsRouteNotReady)
		t.FatalIfErr(err, "The Route was not desired state", "route", names.Route)
	})

	t.Run("e2e", func(t *logging.TLogger) {
		for _, err := range e2eErrors {
			t.FatalIfErr(err, "E2E Failure")
		}
	})
}

// TestContainerExitingMsg is to validate the error condition defined at
// https://github.com/knative/serving/blob/master/docs/spec/errors.md
// for the container crashing scenario.
func TestContainerExitingMsg(legacy *testing.T) {
	t := logging.NewTLogger(legacy)
	defer t.CleanUp()
	t.Parallel()
	const (
		// The given image will always exit with an exit code of 5
		exitCodeReason = "ExitCode5"
		// ... and will print "Crashed..." before it exits
		errorLog = "Crashed..."
	)

	tests := []struct {
		Name           string
		ReadinessProbe *corev1.Probe
	}{{
		Name: "http",
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{},
			},
		},
	}, {
		Name: "tcp",
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{},
			},
		},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.Name, func(t *logging.TLogger) {
			t.Parallel()
			clients := test.Setup(t)
			e2eErrors := make([]error, 0)

			names := test.ResourceNames{
				Config: test.ObjectNameForTest(t),
				Image:  test.Failing,
			}

			defer test.TearDown(clients, names)
			test.CleanupOnInterrupt(func() { test.TearDown(clients, names) })

			t.Run("API", func(t *logging.TLogger) {
				t.V(2).Info("Creating a new Configuration", "configuration", names.Config)

				_, err := v1test.CreateConfiguration(t, clients, names, rtesting.WithConfigReadinessProbe(tt.ReadinessProbe))
				t.FatalIfErr(err, "Failed to create Configuration", "configuration", names.Config)

				t.V(1).Info("When the containers keep crashing, the Configuration should have error status.")

				err := v1test.WaitForConfigurationState(clients.ServingClient, names.Config, func(r *v1.Configuration) (bool, error) {
					cond := r.Status.GetCondition(v1.ConfigurationConditionReady)
					errCtx := [4]interface{}{"configuration", names.Config, "condition", cond}
					ValidateCondition(t.WithValues(errCtx...), cond)
					if cond != nil && !cond.IsUnknown() {
						if cond.IsFalse() && cond.Reason == containerMissing {
							// Spec does not have constraints on the Message
							if !strings.Contains(cond.Message, errorLog) {
								e2eErrors = append(e2eErrors, logging.Error("Bad Condition.Message testing 'crashing container' scenario",
									"wantMessage", errorLog, errCtx...))
							}
							if cond.Message != "" {
								return true, nil
							}
						}
						return true, logging.Error("The configuration was not marked with expected error condition.",
							"wantReason", containerMissing, "wantMessage", "!\"\"", "wantStatus", "False", errCtx...)
					}
					return false, nil
				}, "ConfigContainersCrashing")

				t.FatalIfErr(err, "Failed to validate configuration state")

				revisionName, err := getRevisionFromConfiguration(clients, names.Config)
				t.FatalIfErr(err, "Failed to get revision from configuration", "configuration", names.Config)

				t.V(1).Info("When the containers keep crashing, the revision should have error status.")
				err = v1test.WaitForRevisionState(clients.ServingClient, revisionName, func(r *v1.Revision) (bool, error) {
					cond := r.Status.GetCondition(v1.RevisionConditionReady)
					errCtx := [4]interface{}{"revision", revisionName, "condition", cond}
					ValidateCondition(t.WithValues(errCtx...), cond)
					if cond != nil {
						if cond.Reason == exitCodeReason {
							// Spec does not have constraints on the Message
							if !strings.Contains(cond.Message, errorLog) {
								e2eErrors = append(e2eErrors, logging.Error("Bad Condition.Message testing revision with crashing container",
									"wantMessage", errorLog, errCtx...))
							}
							if cond.Message != "" {
								return true, nil
							}
						}
						return true, logging.Error("The revision was not marked with expected error condition.",
							"wantReason", exitCodeReason, "wantMessage", "!\"\"", errCtx...)
					}
					return false, nil
				}, "RevisionContainersCrashing")

				t.FatalIfErr(err, "Failed to validate revision state")
			})

			t.Run("e2e", func(t *logging.TLogger) {
				for _, err := range e2eErrors {
					t.FatalIfErr(err, "E2E Failure")
				}
			})
		})
	}
}

// Get revision name from configuration.
func getRevisionFromConfiguration(clients *test.Clients, configName string) (string, error) {
	config, err := clients.ServingClient.Configs.Get(configName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if config.Status.LatestCreatedRevisionName != "" {
		return config.Status.LatestCreatedRevisionName, nil
	}
	return "", logging.Error("No valid revision name found", "configuration", configName)
}

var camelCaseRegex = regexp.MustCompile(`^[[:upper:]].*`)
var camelCaseSingleWordRegex = regexp.MustCompile(`^[[:upper:]][^[:whitespace:]]+$`)

func ValidateCondition(t *TLogger, c *apis.Condition) {
	if c == nil {
		return
	}
	if c.Type == "" {
		t.Error("A Condition.Type must not be an empty string")
	} else if !camelCaseRegex.MatchString(c.Type) {
		t.Error("A Condition.Type must be CamelCase, so must start with an upper-case letter")
	}
	if c.Status != apis.ConditionTrue && c.Status != apis.ConditionFalse && c.Status != apis.ConditionUnknown {
		t.Error("A Condition.Status must be True, False, or Unknown")
	}
	if c.Reason != "" && !camelCaseRegex.MatchString(c.Reason) {
		t.Error("A Condition.Reason, if given, must be a single-word CamelCase")
	}
	if c.Severity != apis.ConditionSeverityError && c.Severity != apis.ConditionSeverityWarning && c.Severity != apis.ConditionSeverityInfo {
		t.Error("A Condition.Status must be '', Warning, or Info")
	}
}
