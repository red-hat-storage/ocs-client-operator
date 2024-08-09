#!/bin/bash

CSI_IMAGES_MANIFEST="${CSI_IMAGES_MANIFEST:-config/manager/csi-images.yaml}"

echo "Generating CSI image manifest for OCP versions: ${CSI_OCP_VERSIONS}"

rm -f "${CSI_IMAGES_MANIFEST}"
echo -n "---" > "${CSI_IMAGES_MANIFEST}"
for version in ${CSI_OCP_VERSIONS}; do
  echo "" >> "${CSI_IMAGES_MANIFEST}"

  ver="${version//./_}"
  echo -e "- version: $version\n  containerImages:" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_PROVISIONER_${ver}"
  echo "    provisionerImageURL: \"${!csi_var:-${CSI_IMG_PROVISIONER}}\"" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_ATTACHER_${ver}"
  echo "    attacherImageURL: \"${!csi_var:-${CSI_IMG_ATTACHER}}\"" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_RESIZER_${ver}"
  echo "    resizerImageURL: \"${!csi_var:-${CSI_IMG_RESIZER}}\"" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_SNAPSHOTTER_${ver}"
  echo "    snapshotterImageURL: \"${!csi_var:-${CSI_IMG_SNAPSHOTTER}}\"" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_REGISTRAR_${ver}"
  echo "    driverRegistrarImageURL: \"${!csi_var:-${CSI_IMG_REGISTRAR}}\"" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_CEPH_CSI_${ver}"
  echo "    cephCSIImageURL: \"${!csi_var:-${CSI_IMG_CEPH_CSI}}\"" >> "${CSI_IMAGES_MANIFEST}"

  csi_var="CSI_IMG_ADDONS_${ver}"
  echo "    csiaddonsImageURL: \"${!csi_var:-${CSI_IMG_ADDONS}}\"" >> "${CSI_IMAGES_MANIFEST}"
done

# write per version images to a configmap
rm -f config/manager/csi-images-{v*,multiple}.yaml
template_file=config/manager/csi-images-template.yaml
out_file=config/manager/csi-images-multiple.yaml
for version in ${CSI_OCP_VERSIONS}; do
  export NAME=csi-images-$version

  VER="${version//./_}"
  export VERSION=$version

  provisioner="CSI_IMG_PROVISIONER_${VER}"
  export PROVISIONER=${!provisioner:-${CSI_IMG_PROVISIONER}}

  attacher="CSI_IMG_PROVISIONER_${VER}"
  export ATTACHER=${!attacher:-${CSI_IMG_ATTACHER}}

  resizer="CSI_IMG_RESIZER_${VER}"
  export RESIZER=${!resizer-${CSI_IMG_RESIZER}}

  snapshotter="CSI_IMG_SNAPSHOTTER_${VER}"
  export SNAPSHOTTER=${!snapshotter:-${CSI_IMG_SNAPSHOTTER}}

  registrar="CSI_IMG_REGISTRAR_${VER}"
  export REGISTRAR=${!registrar:-${CSI_IMG_REGISTRAR}}

  plugin="CSI_IMG_CEPH_CSI_${VER}"
  export PLUGIN=${!plugin:-${CSI_IMG_CEPH_CSI}}

  addons="CSI_IMG_ADDONS_${VER}"
  export ADDONS=${!addons:-${CSI_IMG_ADDONS}}

  envsubst < $template_file >> $out_file
done
