---
name: ocs-client-operator-lifecycle
description: Explains ocs-client-operator startup, controllers, StorageClient onboarding and provider sync, CSI driver configuration, Subscription channel validation, and resource cleanup.
---

# OCS Client Operator lifecycle

`ocs-client-operator` is the client-side control plane for consuming storage from a provider cluster. It connects the client to the provider, applies provider-supplied resources, configures CSI for the running client cluster, and reports client status. The CSI stack handles PVC data I/O.

## Process startup

At process startup, the operator determines its namespace and configured watch namespaces. Before constructing the manager, it uses a direct Kubernetes API client to discover which optional APIs are installed. This lets it build a cache and register startup-dependent watches for the APIs the cluster serves.

The process reads the `ocs-tls-profile` in the operator namespace, if present, and uses it to configure the webhook and metrics servers. It then creates the controller-runtime manager with health and metrics endpoints, registers the subscription validating webhook, controller reconcilers, and alert polling runnable, and starts the manager. After manager startup, the controllers reconcile independently.

Optional API handling has two forms. The `StorageClient` reconciler can add some watches dynamically as their APIs become available, including supported snapshot-related APIs, ObjectBucket, and VolumeAttributesClass. ObjectBucketClaim and Ramen MaintenanceMode reconcilers are registered only when those CRDs exist at startup. A CRD-presence reconciler watches those two CRDs and shuts down the process when their presence changes, allowing the next process to rebuild its manager and watches.

## Controller responsibilities

| Controller or runnable | Operand and effect |
| --- | --- |
| `StorageClient` | A cluster-scoped `StorageClient` starts the provider connection and desired-state lifecycle. The reconciler owns supported provider-supplied resources and its status-reporter CronJob. Changes to owned resources can enqueue the `StorageClient` for reconciliation. |
| `OperatorConfigMap` | The `ocs-client-operator-config` ConfigMap drives cluster-level CSI configuration and related operator resources. The reconciler manages delegated CSI resources, companion operator subscriptions, the subscription webhook, console resources, and monitoring resources. StorageClient status and annotations can also enqueue it. OLM manages installation of `ocs-client-operator` itself. |
| CRD presence | A change to the startup-dependent ObjectBucketClaim or MaintenanceMode CRD causes a process restart. Other supported optional watches are added dynamically where the controller supports it. |
| `TLSProfile` | A generation change or deletion of `ocs-tls-profile` causes a process restart so webhook and metrics servers can load the updated TLS configuration. |
| Alert poller | Periodically fetches provider alerts for StorageClients in `Connected` phase with a provider-issued ID. The operator ConfigMap can change the polling interval. |
| `MaintenanceMode` | With the Ramen CRD available, reconciles MaintenanceMode operands and `StorageClient.status.inMaintenanceMode` into provider maintenance requests and Ramen completion status. The current controller expects one StorageClient. |
| `ObjectBucketClaim` | For an OBC whose StorageClass is owned by a StorageClient, adds the client label and finalizer and notifies the provider of claim creation or update. On deletion, it notifies the provider before removing its finalizer. |

## StorageClient and provider workflow

1. A cluster-scoped `StorageClient` supplies `spec.storageProviderEndpoint` and `spec.onboardingTicket`. The controller adds its deletion finalizer, creates a provider client, and, if it has no consumer ID yet, calls `OnboardConsumer` with the ticket, client identity, and cluster information.
2. The provider creates or identifies the client as a `StorageConsumer` and returns its UUID. The operator stores that value in `StorageClient.status.id` (the API's consumer ID field). This UUID is used by later provider calls.
3. The operator sets the phase to `Connected` and ensures a status-reporter CronJob for the client. The reporter is scheduled every minute to send client and cluster status through `ReportStatus`. When the provider returns a changed desired-state hash, the reporter updates a `StorageClient` annotation. That update queues another reconciliation.
4. During active `StorageClient` reconciliation, the controller calls `GetDesiredClientState` using the consumer UUID. The reporter's hash update and changes to owned resources can both enqueue this reconciliation. The response can include Kubernetes object intents, maintenance state, operator channel and desired-state hash, and driver requirements. Depending on enabled capabilities, those objects can supply client connection details and credentials, storage classes, and configuration for snapshots, replication, object storage, or quotas.
5. Supported objects are created or updated locally and receive a controller owner reference to the `StorageClient`. On each desired-state response, the operator compares the returned names with existing objects of each supported kind and prunes owned objects omitted from the response. The operator records provider channel and desired hash annotations and reflects returned driver requirements in `StorageClient` status.

## Deletion and cleanup

Deleting a `StorageClient` enters its offboarding phase. The finalizer holds deletion while the client still has dependent PVs, VolumeSnapshotContents, supported VolumeGroupSnapshotContents, or ObjectBucketClaims. Once those dependencies are gone, the operator calls `OffboardConsumer` when an ID exists, removes the finalizer, and allows Kubernetes deletion and owner-reference cleanup to proceed. The associated status-reporter CronJob and provider-supplied owned resources follow that ownership lifecycle.

Deleting an associated ObjectBucketClaim runs its own provider notification before its finalizer is removed. This lets the provider observe claim removal while the claim is still available to the reconciler.

Deleting `ocs-client-operator-config` starts the ConfigMap controller's cleanup path. It waits for StorageClients to be deleted before removing the CSI resources and related resources it manages, then removes its finalizer. The dependency preserves the StorageClient teardown and provider offboarding path before shared client-side CSI configuration is removed.

## CSI driver configuration

The operator reconciles the CSI operator's shared `OperatorConfig` and individual per-driver `Driver` resources. `OperatorConfig.driverSpecDefaults` provides shared defaults, while a `Driver` resource can set driver-specific values such as controller plugin resource requests and limits. A per-driver value is used for that driver where it overrides a shared default.

The client operator rebuilds the `OperatorConfig` spec during reconciliation and writes selected fields on each `Driver` resource, including controller plugin host networking and operator-managed annotations. Custom values on fields it does not write can remain on the `Driver` resource. Treat fields written by the client operator as operator-controlled. The exact CSI API fields available can vary with the installed CSI operator version.

Provider-reported driver requirements are copied into `StorageClient` status and influence driver enablement and topology configuration. Provider topology labels take precedence over the local topology default when present. The local `ocs-client-operator-config` ConfigMap supplies driver enablement and fallback topology settings.

## Client operator upgrades

The provider can specify the desired client-operator Subscription channel through StorageClient desired state. The client operator records that channel on the StorageClient. Its validating webhook denies a change to the `ocs-client-operator` Subscription when the requested channel conflicts with any StorageClient's non-empty desired channel. A channel change is supported when it matches the provider's desired channel for every StorageClient that specifies one. Once the client operator is on that channel, the config reconciler can align related operator subscriptions with it.

## Notes and variations

- The resources realized for a client depend on the kinds supported by the running operator version, the objects in the provider's current response, and APIs installed on the client cluster. Different versions and enabled capabilities produce different resource sets.
- Objects applied from the provider's desired state belong to the `StorageClient` lifecycle. The `ocs-client-operator-config` controller manages cluster-level CSI configuration using driver requirements reported in `StorageClient` status. It shapes driver enablement, topology labels, and controller host networking.
- An associated ObjectBucketClaim create, update, or deletion sends a notification to the provider. The `StorageClient` reconciler applies any resulting provider-supplied objects on a later desired-state fetch.
- The one-minute interval describes the reporter's schedule. Job startup, the provider call, and the queued `StorageClient` reconciliation add to the time before a change is applied.

