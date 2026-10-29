package ai.alertd.imagethat.remote

import kotlinx.coroutines.test.runTest
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test
import java.security.MessageDigest

class ApiClientTest {
    private lateinit var server: MockWebServer
    private lateinit var client: ApiClient

    @Before fun setUp() {
        server = MockWebServer().also { it.start() }
        client = ApiClient(server.url("/"), null)
    }

    @After fun tearDown() {
        client.close()
        server.shutdown()
    }

    private fun json(body: String, code: Int = 200) = MockResponse().setResponseCode(code)
        .setHeader("Content-Type", "application/json").setBody(body)

    private suspend fun expectError(block: suspend () -> Unit): RemoteError {
        try {
            block()
        } catch (e: RemoteError) {
            return e
        }
        fail("expected RemoteError")
        throw AssertionError()
    }

    @Test fun vaultsParsesServerShape() = runTest {
        server.enqueue(json("""{"vaults":[{"id":"ab12","name":"Holiday","unlocked":true},{"id":"cd34","name":"Work","unlocked":false}]}"""))
        val v = client.vaults()
        assertEquals(listOf(RemoteVault("ab12", "Holiday", true), RemoteVault("cd34", "Work", false)), v)
        val r = server.takeRequest()
        assertEquals("GET", r.method)
        assertEquals("/api/vaults", r.path)
    }

    @Test fun modeReadsInfo() = runTest {
        server.enqueue(json("""{"mode":"client-decrypt","version":"1"}"""))
        assertEquals(ServerMode.CLIENT_DECRYPT, client.mode())
        assertEquals("/api/info", server.takeRequest().path)
        server.enqueue(json("""{"mode":"server-decrypt"}"""))
        assertEquals(ServerMode.SERVER_DECRYPT, client.mode())
    }

    @Test fun modeOfServerWithoutInfoIsServerDecrypt() = runTest {
        server.enqueue(json("""{"error":"no such endpoint"}""", 404))
        assertEquals(ServerMode.SERVER_DECRYPT, client.mode())
    }

    @Test fun loginPostsAccessPasswordAndKeepsCookie() = runTest {
        server.enqueue(json("""{"vaults":[]}""").setHeader("Set-Cookie", "imagethat_session=tok; Path=/; HttpOnly"))
        server.enqueue(json("""{"vaults":[{"id":"ab12","name":"Holiday","unlocked":false}]}"""))
        val pw = "access-pw".toCharArray()
        client.login(pw)
        assertArrayEquals("access-pw".toCharArray(), pw)
        val r = server.takeRequest()
        assertEquals("POST", r.method)
        assertEquals("/api/login", r.path)
        assertTrue(r.body.readUtf8().contains("\"password\":\"access-pw\""))
        client.vaults()
        assertEquals("imagethat_session=tok", server.takeRequest().getHeader("Cookie"))
    }

    @Test fun loginWrongAccessPassword() = runTest {
        server.enqueue(json("""{"error":"wrong password"}""", 401))
        assertEquals(RemoteError.WrongAccessPassword, expectError { client.login("x".toCharArray()) })
    }

    @Test fun unlockPostsJsonPasswordWithEscaping() = runTest {
        server.enqueue(json("""{"vaults":[]}"""))
        val pw = "p\"w\\\né€".toCharArray()
        client.unlock("ab12", pw)
        val r = server.takeRequest()
        assertEquals("POST", r.method)
        assertEquals("/api/vaults/ab12/unlock", r.path)
        assertTrue(r.getHeader("Content-Type")!!.startsWith("application/json"))
        val parsed = kotlinx.serialization.json.Json.parseToJsonElement(r.body.readUtf8())
        assertEquals("p\"w\\\né€",
            (parsed as kotlinx.serialization.json.JsonObject)["password"]!!.let {
                (it as kotlinx.serialization.json.JsonPrimitive).content
            })
    }

    @Test fun unlockLeavesCallersArrayIntact() = runTest {
        // The caller owns its array (so it can retry); the client must not
        // leave a second copy: verified indirectly by caller array intact.
        server.enqueue(json("""{"vaults":[]}"""))
        val pw = "secret".toCharArray()
        client.unlock("ab12", pw)
        assertArrayEquals("secret".toCharArray(), pw)
    }

    @Test fun unlockWrongPassword401() = runTest {
        server.enqueue(json("""{"error":"wrong password"}""", 401))
        assertEquals(RemoteError.WrongPassword, expectError { client.unlock("ab12", "x".toCharArray()) })
    }

    @Test fun unlock429And404() = runTest {
        server.enqueue(json("""{"error":"too many unlock attempts"}""", 429))
        assertEquals(RemoteError.RateLimited, expectError { client.unlock("ab12", "x".toCharArray()) })
        server.enqueue(json("""{"error":"no such vault"}""", 404))
        assertEquals(RemoteError.NotFound, expectError { client.unlock("ab12", "x".toCharArray()) })
    }

    @Test fun listSendsPathAndParsesListing() = runTest {
        server.enqueue(json("""{"path":"a b/c","parent":"a b","entries":[{"name":"x.jpg","isDir":false,"size":12,"mediaType":"image"},{"name":"d","isDir":true,"size":0,"mediaType":""}]}"""))
        val l = client.list("ab12", "a b/c")
        assertEquals("a b/c", l.path)
        assertEquals("a b", l.parent)
        assertEquals(Entry("x.jpg", false, 12, "image"), l.entries[0])
        assertEquals(Entry("d", true, 0, ""), l.entries[1])
        val r = server.takeRequest()
        assertEquals("GET", r.method)
        assertEquals("/api/vaults/ab12/list", r.requestUrl!!.encodedPath)
        assertEquals("a b/c", r.requestUrl!!.queryParameter("path"))
    }

    @Test fun listRootHasNullParent() = runTest {
        server.enqueue(json("""{"path":"","parent":null,"entries":[]}"""))
        val l = client.list("ab12", "")
        assertNull(l.parent)
        assertEquals("", server.takeRequest().requestUrl!!.queryParameter("path"))
    }

    @Test fun lockedOn401() = runTest {
        server.enqueue(json("""{"error":"vault is locked"}""", 401))
        assertEquals(RemoteError.Locked, expectError { client.list("ab12", "") })
    }

    @Test fun forbiddenIsNotLocked() = runTest {
        server.enqueue(json("""{"error":"vault is read-only"}""", 403))
        val e = expectError { client.mkdir("ab12", "x") }
        assertEquals(RemoteError.Forbidden("vault is read-only"), e)
    }

    @Test fun originRejectionIsDistinct() = runTest {
        server.enqueue(json("""{"error":"cross-origin request rejected"}""", 403))
        assertEquals(RemoteError.OriginRejected, expectError { client.mkdir("ab12", "x") })
        server.enqueue(json("""{"error":"bad origin"}""", 403))
        assertEquals(RemoteError.OriginRejected, expectError { client.lock("ab12") })
    }

    @Test fun conflictAndOther() = runTest {
        server.enqueue(json("""{"error":"already exists"}""", 409))
        assertEquals(RemoteError.Exists, expectError { client.mkdir("ab12", "x") })
        server.enqueue(json("""{"error":"list failed"}""", 500))
        assertEquals(RemoteError.Http(500, "list failed"), expectError { client.list("ab12", "") })
    }

    @Test fun openFileSendsRangeAndReturnsLiveResponse() = runTest {
        server.enqueue(MockResponse().setResponseCode(206).setBody("abc"))
        client.openFile("ab12", "d/x.mp4", 5L..7L).use { resp ->
            assertEquals(206, resp.code)
            assertEquals("abc", resp.body!!.string())
        }
        val r = server.takeRequest()
        assertEquals("/api/vaults/ab12/file", r.requestUrl!!.encodedPath)
        assertEquals("d/x.mp4", r.requestUrl!!.queryParameter("path"))
        assertEquals("bytes=5-7", r.getHeader("Range"))
    }

    @Test fun openFileWithoutRangeAndErrorClosesBody() = runTest {
        server.enqueue(MockResponse().setBody("all"))
        client.openFile("ab12", "x", null).use { assertEquals("all", it.body!!.string()) }
        assertNull(server.takeRequest().getHeader("Range"))
        server.enqueue(json("""{"error":"not found"}""", 404))
        assertEquals(RemoteError.NotFound, expectError { client.openFile("ab12", "x", null) })
    }

    @Test fun thumbSendsSizeAndReturnsBytes() = runTest {
        val bytes = byteArrayOf(1, 2, 3, -1)
        server.enqueue(MockResponse().setBody(okio.Buffer().write(bytes)))
        assertArrayEquals(bytes, client.thumb("ab12", "p.jpg", 256))
        val r = server.takeRequest()
        assertEquals("/api/vaults/ab12/thumb", r.requestUrl!!.encodedPath)
        assertEquals("p.jpg", r.requestUrl!!.queryParameter("path"))
        assertEquals("256", r.requestUrl!!.queryParameter("size"))
    }

    @Test fun thumb204IsEmpty() = runTest {
        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(0, client.thumb("ab12", "p.bin", 256).size)
    }

    @Test fun uploadStreamsRawBody() = runTest {
        server.enqueue(json("""{"ok":true,"path":"d/n.jpg","size":3}"""))
        client.upload("ab12", "d", "n.jpg", "xyz".toRequestBody("application/octet-stream".toMediaType()))
        val r = server.takeRequest()
        assertEquals("POST", r.method)
        assertEquals("/api/vaults/ab12/upload", r.requestUrl!!.encodedPath)
        assertEquals("d", r.requestUrl!!.queryParameter("path"))
        assertEquals("n.jpg", r.requestUrl!!.queryParameter("name"))
        assertEquals("xyz", r.body.readUtf8())
    }

    @Test fun mkdirMoveDeleteLockLogout() = runTest {
        repeat(5) { server.enqueue(json("""{"ok":true,"vaults":[]}""")) }
        client.mkdir("ab12", "a/b")
        client.move("ab12", "a", "c")
        client.delete("ab12", "c")
        client.lock("ab12")
        client.logout()
        val mk = server.takeRequest()
        assertEquals("POST /api/vaults/ab12/mkdir", "${mk.method} ${mk.requestUrl!!.encodedPath}")
        assertEquals("""{"path":"a/b"}""", mk.body.readUtf8())
        val mv = server.takeRequest()
        assertEquals("POST /api/vaults/ab12/move", "${mv.method} ${mv.requestUrl!!.encodedPath}")
        assertEquals("""{"from":"a","to":"c"}""", mv.body.readUtf8())
        val del = server.takeRequest()
        assertEquals("DELETE /api/vaults/ab12/entry", "${del.method} ${del.requestUrl!!.encodedPath}")
        assertEquals("c", del.requestUrl!!.queryParameter("path"))
        val lk = server.takeRequest()
        assertEquals("POST /api/vaults/ab12/lock", "${lk.method} ${lk.requestUrl!!.encodedPath}")
        val lo = server.takeRequest()
        assertEquals("POST /api/logout", "${lo.method} ${lo.requestUrl!!.encodedPath}")
    }

    @Test fun cookieSentAfterUnlockAndGoneAfterClose() = runTest {
        server.enqueue(json("""{"vaults":[]}""").addHeader("Set-Cookie", "imagethat_session=tok123; Path=/; HttpOnly; SameSite=Strict"))
        server.enqueue(json("""{"vaults":[]}"""))
        server.enqueue(json("""{"vaults":[]}"""))
        client.unlock("ab12", "x".toCharArray())
        server.takeRequest()
        client.vaults()
        assertEquals("imagethat_session=tok123", server.takeRequest().getHeader("Cookie"))
        client.close()
        client.vaults()
        assertNull(server.takeRequest().getHeader("Cookie"))
    }

    @Test fun noHttpCache() {
        assertNull(client.httpClient.cache)
    }

    @Test fun unreachableIsNetworkError() = runTest {
        val url = server.url("/")
        server.shutdown()
        val c = ApiClient(url, null)
        val e = expectError { c.vaults() }
        assertTrue(e is RemoteError.Network)
        assertTrue((e as RemoteError.Network).cause != null)
        c.close()
        server = MockWebServer().also { it.start() }
    }

    // ---- TLS ----

    private fun tlsServer(): Pair<MockWebServer, String> {
        val held = HeldCertificate.Builder().commonName("localhost").addSubjectAlternativeName("localhost").build()
        val hs = HandshakeCertificates.Builder().heldCertificate(held).build()
        val s = MockWebServer()
        s.useHttps(hs.sslSocketFactory(), false)
        s.start()
        val hash = MessageDigest.getInstance("SHA-256").digest(held.certificate.encoded)
            .joinToString("") { "%02x".format(it) }
        return s to hash
    }

    @Test fun pinnedCorrectHashWorks() = runTest {
        val (s, hash) = tlsServer()
        try {
            s.enqueue(json("""{"vaults":[{"id":"a","name":"n","unlocked":false}]}"""))
            val c = ApiClient(s.url("/"), hash)
            assertEquals(1, c.vaults().size)
            c.close()
        } finally { s.shutdown() }
    }

    @Test fun pinnedWrongHashIsCertMismatchWithActualHash() = runTest {
        val (s, hash) = tlsServer()
        try {
            val c = ApiClient(s.url("/"), "00".repeat(32))
            val e = expectError { c.vaults() }
            assertEquals(RemoteError.CertMismatch(hash), e)
            c.close()
        } finally { s.shutdown() }
    }

    @Test fun selfSignedWithoutPinIsRejected() = runTest {
        val (s, _) = tlsServer()
        try {
            val c = ApiClient(s.url("/"), null)
            assertTrue(expectError { c.vaults() } is RemoteError.Network)
            c.close()
        } finally { s.shutdown() }
    }

    @Test fun probeCertificateReturnsLowercaseHexLeafHash() = runTest {
        val (s, hash) = tlsServer()
        try {
            val got = probeCertificate(s.url("/"))
            assertEquals(hash, got)
            assertEquals(64, got!!.length)
            assertFalse(got.contains(":"))
            assertEquals(got.lowercase(), got)
            assertNull(probeCertificate(server.url("/"))) // plain http: nothing to probe
        } finally { s.shutdown() }
    }
}
