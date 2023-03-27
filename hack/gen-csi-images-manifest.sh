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
