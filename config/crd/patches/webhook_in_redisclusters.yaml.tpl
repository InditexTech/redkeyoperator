# The following patch enables a conversion webhook for the CRD
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: redkeyclusters.redis.inditex.dev
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        caBundle: WEBHOOK_CA_CERT
        service:
          namespace: redkey-operator-webhook
          name: redkey-operator-webhook-service
          path: /convert
      conversionReviewVersions:
      - v1
