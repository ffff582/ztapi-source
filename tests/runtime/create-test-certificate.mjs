import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import forge from 'node-forge';

export function createTestCertificate(
  outputDirectory = 'test-results/tls',
  commonName = 'ztapi.vip',
) {
  const output = resolve(outputDirectory);
  mkdirSync(output, { recursive: true });

  const keys = forge.pki.rsa.generateKeyPair(2048);
  const certificate = forge.pki.createCertificate();
  certificate.publicKey = keys.publicKey;
  certificate.serialNumber = `${Date.now()}`;
  certificate.validity.notBefore = new Date(Date.now() - 60_000);
  certificate.validity.notAfter = new Date(Date.now() + 24 * 60 * 60 * 1000);

  const attributes = [{ name: 'commonName', value: commonName }];
  certificate.setSubject(attributes);
  certificate.setIssuer(attributes);
  certificate.setExtensions([
    { name: 'basicConstraints', cA: false },
    { name: 'keyUsage', digitalSignature: true, keyEncipherment: true },
    { name: 'extKeyUsage', serverAuth: true },
    {
      name: 'subjectAltName',
      altNames: [
        { type: 2, value: 'localhost' },
        { type: 2, value: 'ztapi.vip' },
        { type: 2, value: 'www.ztapi.vip' },
        { type: 7, ip: '127.0.0.1' },
      ],
    },
  ]);
  certificate.sign(keys.privateKey, forge.md.sha256.create());

  const certificatePath = resolve(output, 'fullchain.pem');
  const privateKeyPath = resolve(output, 'privkey.pem');
  writeFileSync(certificatePath, forge.pki.certificateToPem(certificate), {
    mode: 0o644,
  });
  writeFileSync(privateKeyPath, forge.pki.privateKeyToPem(keys.privateKey), {
    mode: 0o600,
  });

  return { certificatePath, privateKeyPath, outputDirectory: output };
}

const invokedPath = process.argv[1] ? resolve(process.argv[1]) : '';
if (invokedPath === fileURLToPath(import.meta.url)) {
  const result = createTestCertificate(process.argv[2]);
  console.log(`Created short-lived TLS material in ${result.outputDirectory}`);
}
