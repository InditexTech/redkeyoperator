/*
  Test that imitates a redis-cluster that was causing rebalancing errors:
  - Value Size: 1Byte - 300KBytes
  - Timeout: 30s
  - Some deletes
 */

import redis from 'k6/x/redis';
import { randomBytes } from 'k6/crypto';
import { check, sleep } from 'k6';

export const options = {
    thresholds: {
        checks: ['rate>0.99'],
    },
};

const client = new redis.Client({
    cluster: {
        nodes: __ENV.REDIS_HOSTS.split(',').map(node => `redis://${node}`),
    },
});

// Helper function to generate random-sized values
function generateRandomValue(maxBytes) {
    const size = Math.floor(Math.random() * maxBytes) + 1; // Random size from 1 to maxBytes
    return randomBytes(size).toString('base64');
}

export default async function () {
    const uniqueKey = `mykey_${__VU}_${__ITER}`;
    const value = generateRandomValue(300000);

    try {
        const setResult = await client.set(uniqueKey, value, 30);
        const setOk = check(setResult, {
            'redis set succeeds': (result) => result === 'OK',
        });
        if (!setOk) {
            const message = `[K6_ERROR] set failed for ${uniqueKey}`;
            console.error(message);
            throw new Error(message);
        }

        // Randomly delete approximately 1 in 10 keys
        if (Math.random() < 0.1) {
            const deleted = await client.del(uniqueKey);
            const deleteOk = check(deleted, {
                'redis delete succeeds': (result) => result === 1,
            });
            if (!deleteOk) {
                const message = `[K6_ERROR] delete failed for ${uniqueKey}; result=${deleted}`;
                console.error(message);
                throw new Error(message);
            }
        } else {
            const retrievedValue = await client.get(uniqueKey);
            const getOk = check(retrievedValue, {
                'redis get returns original value': (result) => result === value,
            });
            if (!getOk) {
                const resultLength = retrievedValue ? retrievedValue.length : 'null';
                const message = `[K6_ERROR] get failed for ${uniqueKey}; length=${resultLength}`;
                console.error(message);
                throw new Error(message);
            }
        }
    } catch (error) {
        const message = `[K6_ERROR] iteration failed for ${uniqueKey}: ${error}`;
        console.error(message);
        throw error;
    }

    // Sleep for a short random duration to simulate real-world load patterns
    sleep(Math.random() * 0.1); // Sleep for up to 100ms
}
