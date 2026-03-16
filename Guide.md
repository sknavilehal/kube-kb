Everything is installed. Let's go.

---

## Step 1 — Initialize the Project

Run this inside `/Users/shamanth/projects/kube-kb`:

```bash
kubebuilder init --domain kubekb.io --repo github.com/shamanth/kube-kb
```

**What each flag means:**
- `--domain kubekb.io` — your CRD's API group will be `*.kubekb.io`. So your custom resource will be `knowledgebases.kubekb.io`
- `--repo` — the Go module name (matches what goes in `go.mod`)

**What gets generated:**

```
kube-kb/
├── cmd/
│   └── main.go          ← entry point, starts the controller manager
├── config/              ← all Kubernetes YAML manifests
│   ├── default/         ← kustomize base for deploying the operator
│   ├── manager/         ← the operator Deployment manifest
│   ├── rbac/            ← ClusterRole, ServiceAccount, bindings
│   └── ...
├── Makefile             ← build, test, deploy commands
├── go.mod
└── go.sum
```

---

## Step 2 — Create the API (CRD + Controller)

```bash
kubebuilder create api --group kb --version v1alpha1 --kind KnowledgeBase
```

**What each flag means:**
- `--group kb` — the group prefix → full group is `kb.kubekb.io`
- `--version v1alpha1` — your API version. Convention: start with `v1alpha1`, stabilize to `v1beta1`, then `v1`
- `--kind KnowledgeBase` — the name of your custom resource type

It will ask two questions — say **yes** to both:
```
Create Resource [y/n]: y    ← generates the CRD struct
Create Controller [y/n]: y  ← generates the reconcile loop
```

**New files added:**

```
kube-kb/
├── api/
│   └── v1alpha1/
│       ├── knowledgebase_types.go    ← YOU DEFINE YOUR CRD SCHEMA HERE
│       └── zz_generated.deepcopy.go ← auto-generated, never touch
├── internal/
│   └── controller/
│       └── knowledgebase_controller.go  ← YOUR RECONCILE LOOP LIVES HERE
└── config/
    └── crd/                             ← generated CRD YAML manifests
```

---

## Step 3 — Understanding the Generated Code (Deep Dive)

### `api/v1alpha1/knowledgebase_types.go`

This is where you define what a `KnowledgeBase` YAML looks like:

```go
type KnowledgeBaseSpec struct {
    // What the USER puts in their YAML goes here
    RepoURL   string `json:"repoURL"`
    ModelName string `json:"modelName,omitempty"`
}

type KnowledgeBaseStatus struct {
    // What the OPERATOR writes back to report state
    Phase   string `json:"phase,omitempty"`   // e.g. "Ingesting", "Ready", "Error"
    Message string `json:"message,omitempty"`
}

type KnowledgeBase struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   KnowledgeBaseSpec   `json:"spec,omitempty"`
    Status KnowledgeBaseStatus `json:"status,omitempty"`
}
```

**Kubernetes mechanic — Spec vs Status:**
- `Spec` = **desired state** — written by the user
- `Status` = **observed state** — written by your controller
- This split is a core Kubernetes API convention. Controllers never modify Spec; they only read it and write Status

---

### `internal/controller/knowledgebase_controller.go`

```go
func (r *KnowledgeBaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // req.NamespacedName = "namespace/name" of the CR that changed

    // 1. Fetch the CR
    kb := &v1alpha1.KnowledgeBase{}
    if err := r.Get(ctx, req.NamespacedName, kb); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Do your logic here
    // ...

    return ctrl.Result{}, nil
}
```

**Kubernetes mechanic — The Reconcile Loop:**

This is the most important concept to understand. Kubernetes works on **level-triggered** logic, not **event-triggered**:

- Your `Reconcile` function is called when **anything changes** on a `KnowledgeBase` resource
- It is NOT called with "what changed" — you always get the **full current state**
- Your job: look at the current state, compare to desired state, take action to close the gap
- The function must be **idempotent** — running it 10 times in a row should have the same result as running it once

```
User creates KnowledgeBase CR
        ↓
Kubernetes API Server stores it in etcd
        ↓
Your controller's Watch detects a change
        ↓
Reconcile(ctx, req) is called
        ↓
You: Get the CR → check what exists → create/update what's missing
        ↓
Return ctrl.Result{} → done (or RequeueAfter to re-run later)
```

**`ctrl.Result` options:**

```go
return ctrl.Result{}, nil                          // done, wait for next change
return ctrl.Result{RequeueAfter: 30 * time.Second} // re-run in 30s (for polling)
return ctrl.Result{Requeue: true}, nil             // re-run immediately
return ctrl.Result{}, err                          // something failed, retry with backoff
```

---

### `cmd/main.go`

```go
mgr, _ := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{...})

(&KnowledgeBaseReconciler{
    Client: mgr.GetClient(),
    Scheme: mgr.GetScheme(),
}).SetupWithManager(mgr)

mgr.Start(ctrl.SetupSignalHandler())
```

**Kubernetes mechanic — The Manager:**
- The `Manager` is the runtime that hosts your controllers
- It handles: connecting to the API server, caching objects locally, leader election (so only one replica of your operator runs at a time), and graceful shutdown
- `mgr.GetClient()` gives you a client that reads from a **local cache** (not the API server directly) — this is important for performance

---

## Step 4 — Install CRDs and Run Locally

```bash
# Install your CRD into the cluster
make install

# Run the controller locally (not inside Kubernetes yet)
make run
```

`make run` runs your controller on your laptop but connected to the Docker Desktop cluster. This is the fastest dev loop — no Docker build needed.

Then in another terminal:
```bash
kubectl apply -f - <<EOF
apiVersion: kb.kubekb.io/v1alpha1
kind: KnowledgeBase
metadata:
  name: my-kb
spec:
  repoURL: "https://github.com/your/docs-repo"
  modelName: "llama3.2"
EOF
```

Watch your controller's logs react to it in real time.

---

## The Mental Model to Always Keep

```
etcd (source of truth)
    ↕
API Server (the gatekeeper — all reads/writes go through here)
    ↕
Controller Manager (your operator watches here)
    ↓
Reconcile Loop (your code) → creates Pods, Services, ConfigMaps, etc.
```

Your operator never talks to Pods or nodes directly. It **only talks to the API server**, and the API server + kubelet on each node handles the actual execution.

---

Ready to switch to Agent mode and scaffold it? Once you do, I'll walk you through filling in the `KnowledgeBaseSpec` fields and writing the first real reconcile logic.