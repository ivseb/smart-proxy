#!/bin/bash

# Configuration
CLUSTER_CONTEXT="docker-desktop" # Change to "kind-kind" or "minikube" if using those
NAMESPACE="smart-proxy-demo"
IMAGE_NAME="isebben/smart-proxy:latest"

echo "🚀 Setting up Local Dev Environment for Smart Proxy V2.5..."

# 1. Switch Context
echo "👉 Switching K8s context to $CLUSTER_CONTEXT..."
kubectl config use-context $CLUSTER_CONTEXT
if [ $? -ne 0 ]; then
    echo "❌ Failed to switch context. Ensure Docker Desktop K8s is running."
    exit 1
fi

# 2. Install Ingress Controller (Nginx) - Critical for local domains on port 80
echo "👉 Installing Ingress Controller (Nginx)..."
# Check if already installed to save time
kubectl get ns ingress-nginx > /dev/null 2>&1
if [ $? -ne 0 ]; then
    kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.8.2/deploy/static/provider/cloud/deploy.yaml
    echo "⏳ Waiting for Ingress Controller to be ready..."
    kubectl wait --namespace ingress-nginx \
      --for=condition=ready pod \
      --selector=app.kubernetes.io/component=controller \
      --timeout=90s
else
    echo "✅ Ingress Controller already installed."
fi

# 3. Create Namespace
echo "👉 Creating namespace $NAMESPACE..."
kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

# 3. Build Docker Image
echo "🔨 Building Smart Proxy Docker image..."
docker build -t $IMAGE_NAME .
if [ $? -ne 0 ]; then
    echo "❌ Build failed!"
    exit 1
fi

# 4. Cleanup old deployment if exists
echo "🧹 Cleaning up old deployment..."
kubectl delete deployment smart-proxy -n $NAMESPACE --ignore-not-found
kubectl delete service smart-proxy -n $NAMESPACE --ignore-not-found
kubectl delete role smart-proxy-role -n $NAMESPACE --ignore-not-found
kubectl delete rolebinding smart-proxy-binding -n $NAMESPACE --ignore-not-found
kubectl delete serviceaccount smart-proxy-sa -n $NAMESPACE --ignore-not-found

# 5. Apply RBAC (Modifying on the fly to match our demo namespace)
echo "🛡️ Applying RBAC..."
cat deploy/kubernetes/rbac.yaml | sed "s/namespace: smart-proxy/namespace: $NAMESPACE/g" | sed "s/namespace: test-namespace/namespace: $NAMESPACE/g" | kubectl apply -f -

# 6. Apply Service
echo "🌐 Applying Proxy Service..."
cat deploy/kubernetes/service.yaml | sed "s/namespace: smart-proxy/namespace: $NAMESPACE/g" | kubectl apply -f -

# 7. Apply Deployment (Injecting Image Pull Policy for local dev)
echo "📦 Applying Proxy Deployment..."
cat deploy/kubernetes/deployment.yaml | \
sed "s|namespace: smart-proxy|namespace: $NAMESPACE|g" | \
sed "s|isebben/smart-proxy:latest|$IMAGE_NAME|g" | \
sed "s|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|g" | \
sed '/env:/d' > deploy/kubernetes/deployment.temp.yaml

kubectl apply -f deploy/kubernetes/deployment.temp.yaml
rm deploy/kubernetes/deployment.temp.yaml
kubectl set env deployment/smart-proxy WATCH_NAMESPACE=$NAMESPACE -n $NAMESPACE

# ---------------------------------------------------------
# 8. Deploy Demo Application (Cascading Dependencies)
#    Structure: frontend -> backend -> redis
# ---------------------------------------------------------
echo "🧪 Deploying 3-Tier Demo App..."

# Redis (Database)
echo "   - Redis (Database)..."
kubectl create deployment redis --image=redis:alpine --replicas=1 -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
kubectl expose deployment redis --port=6379 -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

# Backend (Simulated API)
echo "   - Backend (API)..."
kubectl create deployment backend --image=nginx:alpine --replicas=1 -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
kubectl expose deployment backend --port=80 -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

# Frontend (Web App)
echo "   - Frontend (Web)..."
kubectl create deployment frontend --image=nginx:alpine --replicas=1 -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
kubectl expose deployment frontend --port=80 -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

# Create Ingress for Frontend (To be Patched)
echo "   - Creating Ingress for 'frontend.local'..."
kubectl delete ingress frontend-ingress -n $NAMESPACE --ignore-not-found # Ensure clean state (no stale annotations)
cat <<EOF | kubectl apply -n $NAMESPACE -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: frontend-ingress
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  ingressClassName: nginx
  rules:
  - host: frontend.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: frontend
            port:
              number: 80
EOF

# Create Ingress for 'admin.local' (Smart Proxy Dashboard)
echo "   - Creating Ingress for 'admin.local'..."
kubectl delete ingress admin-ingress -n $NAMESPACE --ignore-not-found # Ensure clean state
cat <<EOF | kubectl apply -n $NAMESPACE -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: admin-ingress
spec:
  ingressClassName: nginx
  rules:
  - host: admin.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: smart-proxy
            port:
              number: 8081
EOF

echo "✅ Setup Complete!"
echo "---------------------------------------------------"
echo "to access the Admin UI:
   Ensure '127.0.0.1 admin.local' is in your /etc/hosts via:
   sudo sh -c 'echo "127.0.0.1 admin.local" >> /etc/hosts'
   Then open http://admin.local

to test Patching:
   1. Ensure '127.0.0.1 frontend.local' is in your /etc/hosts
   2. Open Admin UI -> 'Ingress Patching' -> Click 'Patch' on frontend-ingress
   3. Visit http://frontend.local"
echo "---------------------------------------------------"
