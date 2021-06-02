# scheduler-extender

An example project for running a [kube-scheduler extender][scheduler-extender]
webhook handlers in a kubebuilder webhook server.

[scheduler-extender]: https://github.com/kubernetes/community/blob/master/contributors/design-proposals/scheduling/scheduler_extender.md

## Test

`tests/e2e/` contains e2e test for the scheduler extender using [kuttl][kuttl].
The kuttl test manifests can be used to manually set up an environment with a
kube-scheduler configured to use the scheduler extender webhook endpoints.

The test creates a 5 node kind cluster, with the control-plane tained,
unschedulable by default. A static pod with scheduler name `demo-scheduler` is
created and checked if it's scheduled on a target node. The scheduler extender
is configured by setting `target` and `allowed` values in a configmap
(`game-demo` in the test case). `allowed` is a list of nodes that are filtered
by the filter endpoint. `target` is one target node on which the given pod must
be scheduled. While scoring the nodes, the target node is given a score of 10
and all the other nodes are scored 5. The test checks to ensure that the pod
is always scheduled on the target node.

[kuttl]: https://kuttl.dev/
