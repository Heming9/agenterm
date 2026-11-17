//! DTLS implementation using dimpl with RustCrypto backend.

use std::sync::Arc;
use std::time::Instant;

use p256::ecdsa::{DerSignature, SigningKey, signature::Signer};
use p256::elliptic_curve::rand_core::{OsRng, RngCore};
use p256::pkcs8::EncodePrivateKey;
use rcgen::{CertificateParams, DnType, IsCa, PKCS_ECDSA_P256_SHA256};
use str0m_proto::crypto::CryptoError;
use str0m_proto::crypto::DtlsVersion;
use str0m_proto::crypto::dtls::ProtocolVersion;
use str0m_proto::crypto::dtls::{DtlsCert, DtlsImplError, DtlsInstance, DtlsOutput, DtlsProvider};

// ============================================================================
// DTLS Provider Implementation
// ============================================================================

#[derive(Debug)]
pub(super) struct RustCryptoDtlsProvider;

struct CertificateKey {
    private: SigningKey,
    public: p256::EncodedPoint,
}

impl rcgen::PublicKeyData for CertificateKey {
    fn der_bytes(&self) -> &[u8] {
        self.public.as_bytes()
    }

    fn algorithm(&self) -> &'static rcgen::SignatureAlgorithm {
        &PKCS_ECDSA_P256_SHA256
    }
}

impl rcgen::SigningKey for CertificateKey {
    fn sign(&self, message: &[u8]) -> Result<Vec<u8>, rcgen::Error> {
        let signature: DerSignature = self
            .private
            .try_sign(message)
            .map_err(|_| rcgen::Error::RemoteKeyError)?;
        Ok(signature.as_bytes().to_vec())
    }
}

impl DtlsProvider for RustCryptoDtlsProvider {
    fn generate_certificate(&self) -> Option<DtlsCert> {
        let mut rng = OsRng;
        let mut serial = [0; 16];
        rng.try_fill_bytes(&mut serial).ok()?;
        let private = SigningKey::random(&mut rng);
        let public = private.verifying_key().to_encoded_point(false);
        let key = CertificateKey { private, public };

        let mut params = CertificateParams::default();
        params
            .distinguished_name
            .push(DnType::OrganizationName, "DTLS");
        params
            .distinguished_name
            .push(DnType::CommonName, "DTLS Peer");
        params.is_ca = IsCa::NoCa;
        params.not_before = time::OffsetDateTime::now_utc();
        params.not_after = params.not_before + time::Duration::days(365);
        params.serial_number = Some(serial.to_vec().into());
        let certificate = params.self_signed(&key).ok()?;

        Some(DtlsCert {
            certificate: certificate.der().to_vec(),
            private_key: key.private.to_pkcs8_der().ok()?.as_bytes().to_vec(),
        })
    }

    fn new_dtls(
        &self,
        cert: &DtlsCert,
        now: Instant,
        dtls_version: DtlsVersion,
        mtu: Option<usize>,
    ) -> Result<Box<dyn DtlsInstance>, CryptoError> {
        let dimpl_cert = dimpl::DtlsCertificate {
            certificate: cert.certificate.clone(),
            private_key: cert.private_key.clone(),
        };

        // Create a default dimpl Config with RustCrypto crypto provider
        // ICE verifies return routability before DTLS, making server cookies redundant.
        let mut builder = dimpl::Config::builder().use_server_cookie(false);
        if let Some(mtu) = mtu {
            builder = builder.mtu(mtu);
        }
        if self.is_test() {
            // We need the DTLS impl to be deterministic for the BWE tests.
            builder = builder.dangerously_set_rng_seed(42);
        }

        let config = builder
            .build()
            .map_err(|e| CryptoError::Other(format!("dimpl config creation failed: {}", e)))?;

        let config = Arc::new(config);
        let dtls = match dtls_version {
            DtlsVersion::Dtls12 => dimpl::Dtls::new_12(config, dimpl_cert, now),
            DtlsVersion::Dtls13 => dimpl::Dtls::new_13(config, dimpl_cert, now),
            DtlsVersion::Auto => dimpl::Dtls::new_auto(config, dimpl_cert, now),
            _ => {
                return Err(CryptoError::Other(format!(
                    "Unsupported DTLS version: {dtls_version}"
                )));
            }
        };

        Ok(Box::new(RustCryptoDtlsInstance { dtls }))
    }
}

// ============================================================================
// DTLS Instance Wrapper
// ============================================================================

struct RustCryptoDtlsInstance {
    dtls: dimpl::Dtls,
}

impl std::fmt::Debug for RustCryptoDtlsInstance {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("RustCryptoDtlsInstance").finish()
    }
}

impl DtlsInstance for RustCryptoDtlsInstance {
    fn set_active(&mut self, active: bool) {
        self.dtls.set_active(active);
    }

    fn handle_packet(&mut self, packet: &[u8]) -> Result<(), DtlsImplError> {
        self.dtls.handle_packet(packet)
    }

    fn poll_output<'a>(&mut self, buf: &'a mut [u8]) -> DtlsOutput<'a> {
        self.dtls.poll_output(buf)
    }

    fn handle_timeout(&mut self, now: Instant) -> Result<(), DtlsImplError> {
        self.dtls.handle_timeout(now)
    }

    fn send_application_data(&mut self, data: &[u8]) -> Result<(), DtlsImplError> {
        self.dtls.send_application_data(data)
    }

    fn is_active(&self) -> bool {
        self.dtls.is_active()
    }

    fn protocol_version(&self) -> Option<ProtocolVersion> {
        self.dtls.protocol_version()
    }

    fn is_closing(&self) -> bool {
        self.dtls.is_closing()
    }

    fn is_closed(&self) -> bool {
        self.dtls.is_closed()
    }

    fn close(&mut self) -> Result<(), DtlsImplError> {
        self.dtls.close()
    }
}
