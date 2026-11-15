export function getCreatedTokenCredential(response, name) {
  const payload = response?.data;
  const credential = payload?.data;
  if (
    !payload?.success ||
    typeof credential?.key !== 'string' ||
    credential.key === ''
  ) {
    return null;
  }

  return {
    id: credential.id,
    name,
    key: credential.key,
    keyPrefix: credential.key_prefix,
  };
}
