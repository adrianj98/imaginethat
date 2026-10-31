package ai.alertd.imagethat.remote

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** The client-decrypt path's errors come from the Go engine as coded messages (core/errcode). */
class GoRemoteErrorTest {
    private fun map(m: String) = goRemoteError(Exception(m))

    @Test fun codesMapToRemoteErrors() {
        assertEquals(RemoteError.WrongAccessPassword, map("wrong_access_password: remote: wrong server access password"))
        assertEquals(RemoteError.WrongPassword, map("wrong_password: remote: wrong password"))
        assertEquals(RemoteError.Locked, map("locked: remote: vault is locked on the server"))
        assertEquals(RemoteError.NotFound, map("not_found: remote: not found (vaults: nope)"))
        assertEquals(RemoteError.Exists, map("exists: remote: already exists"))
        assertEquals(RemoteError.CertMismatch(""), map("cert_mismatch: remote: server certificate does not match the pinned fingerprint"))
        assertTrue(map("forbidden: remote: the server refused the change") is RemoteError.Forbidden)
    }

    @Test fun uncodedStatusesAndTransportFailures() {
        assertEquals(RemoteError.RateLimited, map("remote: login: HTTP 429"))
        assertEquals(RemoteError.Http(500, "vaults failed"), map("remote: vaults: HTTP 500: vaults failed"))
        assertEquals(RemoteError.Http(502, ""), map("remote: info: HTTP 502"))
        val e = map("remote: Get \"https://10.0.0.2:8347/api/vaults\": dial tcp 10.0.0.2:8347: connect: connection refused")
        assertTrue(e is RemoteError.Network)
        assertTrue(e.cause != null)
    }

    @Test fun remoteErrorsPassThrough() {
        assertEquals(RemoteError.Locked, goRemoteError(RemoteError.Locked))
    }
}
