package ai.alertd.imagethat.remote

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import java.net.InetSocketAddress
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
import javax.net.ssl.TrustManager
import javax.net.ssl.X509TrustManager

/** SHA-256 of the DER-encoded certificate as lowercase hex with no separators (64 chars). */
fun sha256Hex(cert: X509Certificate): String =
    MessageDigest.getInstance("SHA-256").digest(cert.encoded).joinToString("") { "%02x".format(it) }

/** Thrown by [PinnedTrustManager]; carries the leaf hash actually presented. */
internal class PinMismatchException(val actual: String) : CertificateException("pin mismatch: $actual")

/**
 * Trusts exactly one leaf certificate, identified by SHA-256. Self-signed
 * servers do not chain to any CA, so no chain or validity-period validation is
 * done: the pin is the sole root of trust.
 */
internal class PinnedTrustManager(pinnedSha256: String) : X509TrustManager {
    private val pin = pinnedSha256.lowercase().replace(":", "")

    override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {
        if (chain.isEmpty()) throw CertificateException("empty certificate chain")
        val actual = sha256Hex(chain[0])
        if (!MessageDigest.isEqual(actual.toByteArray(), pin.toByteArray())) throw PinMismatchException(actual)
    }

    override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) =
        throw CertificateException("client certificates are not supported")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/**
 * With a pin, the certificate itself is the identity, so the name it was
 * issued for is irrelevant (self-signed certs are commonly for a LAN IP/host
 * the user typed). Only used when pinned; unpinned connections keep the
 * platform hostname verification.
 */
internal val pinnedHostnameVerifier = HostnameVerifier { _, _ -> true }

internal fun sslContextFor(tm: TrustManager): SSLContext =
    SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), SecureRandom()) }

/**
 * Handshake with [url] and return the leaf certificate's SHA-256 (lowercase
 * hex, no colons), or null if [url] is not https or the handshake fails. The
 * certificate is accepted only for the duration of this handshake; no HTTP
 * request is sent and nothing is remembered, so it is not trusted for anything
 * else. The caller shows the fingerprint to the user, who decides whether to pin.
 */
suspend fun probeCertificate(url: HttpUrl): String? {
    if (!url.isHttps) return null
    return withContext(Dispatchers.IO) {
        var leaf: X509Certificate? = null
        val capture = object : X509TrustManager {
            override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {
                leaf = chain.firstOrNull()
            }
            override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) = Unit
            override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
        }
        try {
            val factory = sslContextFor(capture).socketFactory
            val raw = java.net.Socket()
            raw.connect(InetSocketAddress(url.host, url.port), 10_000)
            raw.soTimeout = 10_000
            (factory.createSocket(raw, url.host, url.port, true) as SSLSocket).use { it.startHandshake() }
            leaf?.let(::sha256Hex)
        } catch (e: java.io.IOException) {
            null
        } catch (e: java.security.GeneralSecurityException) {
            null
        }
    }
}
