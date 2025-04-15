#!/bin/bash

rm -rf catalog
rm -rf catalog.Dockerfile

mkdir catalog

${OPM} render --output=yaml ${BUNDLE_IMG} > catalog/ocs-client-bundle.yaml
${OPM} render --output=yaml ${CSI_ADDONS_BUNDLE_IMG} > catalog/csi-adddons-bundle.yaml
${OPM} render --output=yaml ${CEPH_CSI_BUNDLE_IMG} > catalog/cephcsi-operator-bundle.yaml
${OPM} render --output=yaml ${RECIPE_BUNDLE_IMG} > catalog/recipe.yaml
${OPM} render --output=yaml ${NOOBAA_BUNDLE_IMG} > catalog/noobaa-operator-bundle.yaml

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
---
defaultChannel: alpha
name: $NOOBAA_BUNDLE_NAME
schema: olm.package
---
schema: olm.channel
package: noobaa-operator
name: alpha
entries:
  - name: $NOOBAA_BUNDLE_NAME.$NOOBAA_BUNDLE_VERSION
---
defaultChannel: alpha
name: $CEPH_CSI_BUNDLE_NAME
schema: olm.package
---
schema: olm.channel
package: cephcsi-operator
name: alpha
entries:
  - name: $CEPH_CSI_BUNDLE_NAME.$CEPH_CSI_BUNDLE_VERSION
---
defaultChannel: alpha
name: $RECIPE_BUNDLE_NAME
schema: olm.package
---
schema: olm.channel
package: recipe
name: alpha
entries:
  - name: $RECIPE_BUNDLE_NAME.v$RECIPE_BUNDLE_VERSION
EOF

${OPM} validate catalog
${OPM} generate dockerfile catalog
${IMAGE_BUILD_CMD} build --platform="linux/amd64" -t ${CATALOG_IMG} -f catalog.Dockerfile .
