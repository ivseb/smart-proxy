{{/* Chart name (optionally overridden). */}}
{{- define "smart-proxy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "smart-proxy.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "smart-proxy.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "smart-proxy.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{/* Selector labels (immutable across upgrades). */}}
{{- define "smart-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "smart-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: {{ include "smart-proxy.name" . }}
{{- end -}}

{{/* Name of the ServiceAccount to use. */}}
{{- define "smart-proxy.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (printf "%s-sa" (include "smart-proxy.fullname" .)) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Secret holding the dashboard credentials (generated unless auth.existingSecret is set). */}}
{{- define "smart-proxy.authSecretName" -}}
{{- default (printf "%s-auth" (include "smart-proxy.fullname" .)) .Values.auth.existingSecret -}}
{{- end -}}

{{/* Public URL of the admin dashboard, from the Route or Ingress settings. */}}
{{- define "smart-proxy.adminURL" -}}
{{- if .Values.route.enabled -}}
https://{{ .Values.route.host }}
{{- else if .Values.ingress.enabled -}}
http{{ if .Values.ingress.tls }}s{{ end }}://{{ .Values.ingress.host }}
{{- end -}}
{{- end -}}

{{/* Validates the auth settings and fails the install with a readable message. */}}
{{- define "smart-proxy.validateAuth" -}}
{{- $modes := list "none" "basic" "token" "oidc" "header" "openshift" -}}
{{- if not (has .Values.auth.mode $modes) -}}
{{- fail (printf "auth.mode must be one of %s (got %q)" (join ", " $modes) .Values.auth.mode) -}}
{{- end -}}
{{- if eq .Values.auth.mode "oidc" -}}
{{- $o := .Values.auth.oidc -}}
{{- if or (not $o.issuerURL) (not $o.clientID) -}}
{{- fail "auth.mode=oidc requires auth.oidc.issuerURL and auth.oidc.clientID" -}}
{{- end -}}
{{- if and (not $o.clientSecret) (not .Values.auth.existingSecret) -}}
{{- fail "auth.mode=oidc requires auth.oidc.clientSecret (or auth.existingSecret with an oidc-client-secret key)" -}}
{{- end -}}
{{- if and (not $o.redirectURL) (not (include "smart-proxy.adminURL" .)) -}}
{{- fail "auth.mode=oidc requires auth.oidc.redirectURL when neither route nor ingress is enabled" -}}
{{- end -}}
{{- if not (or $o.allowAll $o.allowedEmails $o.allowedDomains $o.allowedGroups) -}}
{{- fail "auth.mode=oidc requires auth.oidc.allowedEmails, allowedDomains, allowedGroups or allowAll=true" -}}
{{- end -}}
{{- end -}}
{{- if and (eq .Values.auth.mode "openshift") (not (or .Values.route.enabled .Values.ingress.enabled)) -}}
{{- fail "auth.mode=openshift requires route.enabled or ingress.enabled (the OAuth redirect needs a public host)" -}}
{{- end -}}
{{- end -}}

{{/* "true" when Smart Proxy watches all namespaces (optionally filtered by a label selector). */}}
{{- define "smart-proxy.clusterWide" -}}
{{- if or .Values.config.allNamespaces .Values.config.namespaceSelector -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/* JSON list of the explicitly watched namespaces (the release namespace by default). */}}
{{- define "smart-proxy.watchedNamespaces" -}}
{{- $list := .Values.config.watchNamespaces | default list -}}
{{- if and (not $list) .Values.config.watchNamespace -}}
{{- $list = list .Values.config.watchNamespace -}}
{{- end -}}
{{- if not $list -}}
{{- $list = list .Release.Namespace -}}
{{- end -}}
{{- $list | uniq | toJson -}}
{{- end -}}

{{/* Permissions Smart Proxy needs in each managed namespace. */}}
{{- define "smart-proxy.rbacRules" -}}
- apiGroups: ["apps", "extensions"]
  resources: ["deployments", "deployments/scale"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: [""]
  resources: ["services", "pods"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses"]
  verbs: ["get", "list", "watch", "update", "patch"]
# Read HPAs to wake deployments at their minReplicas and to detect KEDA-managed ones.
- apiGroups: ["autoscaling"]
  resources: ["horizontalpodautoscalers"]
  verbs: ["get", "list", "watch"]
{{- if .Values.rbac.openshiftRoutes }}
- apiGroups: ["route.openshift.io"]
  resources: ["routes"]
  verbs: ["get", "list", "watch", "update", "patch"]
{{- end }}
{{- end }}
