#!/bin/bash

rm -rf catalog
rm -rf catalog.Dockerfile

mkdir catalog

${OPM} render --output=yaml ${BUNDLE_IMG} > catalog/ocs-client-bundle.yaml
${OPM} render --output=yaml ${CSI_ADDONS_BUNDLE_IMG} > catalog/csi-adddons-bundle.yaml

cat << EOF >> catalog/index.yaml
---
defaultChannel: alpha
name: $IMAGE_NAME
schema: olm.package
---
schema: olm.channel
package: ocs-client-operator
name: alpha
entries:
  - name: $IMAGE_NAME.v$VERSION
---
defaultChannel: alpha
name: $CSI_ADDONS_PACKAGE_NAME
schema: olm.package
---
schema: olm.channel
package: csi-addons
name: alpha
entries:
  - name: $CSI_ADDONS_PACKAGE_NAME.v$CSI_ADDONS_PACKAGE_VERSION
EOF

${OPM} validate catalog
${OPM} generate dockerfile catalog
${IMAGE_BUILD_CMD} build --platform="linux/amd64" -t ${CATALOG_IMG} -f catalog.Dockerfile .
