OUT_DIR=bundle/manifests
mkdir -p ${OUT_DIR}
for version in ${CSI_OCP_VERSIONS}; do
  NAME=csi-images-$version
  OUT_FILE=${OUT_DIR}/$NAME

  VER="${version//./_}"
  VERSION=$version

  provisioner="CSI_IMG_PROVISIONER_${VER}"
  PROVISIONER=${!provisioner:-${CSI_IMG_PROVISIONER}}

  attacher="CSI_IMG_ATTACHER_${VER}"
  ATTACHER=${!attacher:-${CSI_IMG_ATTACHER}}

  resizer="CSI_IMG_RESIZER_${VER}"
  RESIZER=${!resizer-${CSI_IMG_RESIZER}}

  snapshotter="CSI_IMG_SNAPSHOTTER_${VER}"
  SNAPSHOTTER=${!snapshotter:-${CSI_IMG_SNAPSHOTTER}}

  registrar="CSI_IMG_REGISTRAR_${VER}"
  REGISTRAR=${!registrar:-${CSI_IMG_REGISTRAR}}

  plugin="CSI_IMG_CEPH_CSI_${VER}"
  PLUGIN=${!plugin:-${CSI_IMG_CEPH_CSI}}

  addons="CSI_IMG_ADDONS_${VER}"
  ADDONS=${!addons:-${CSI_IMG_ADDONS}}

  echo "\
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: $NAME
  labels:
    ocs.openshift.io/csi-images-version: $VERSION
data:
  provisioner: "$PROVISIONER"
  attacher: "$ATTACHER"
  resizer: "$RESIZER"
  snapshotter: "$SNAPSHOTTER"
  registrar: "$REGISTRAR"
  plugin: "$PLUGIN"
  addons: "$ADDONS"
" > $OUT_FILE

done
