-- Federation: a paired server's address as browsers and the signatures
-- write it (federation.Origin): lower case, without the scheme's default
-- port. Pairings made before addresses were normalized match again.
UPDATE peers SET address = lower(address);
UPDATE peers SET address = substr(address, 1, length(address) - 4) WHERE address LIKE 'https://%:443';
UPDATE peers SET address = substr(address, 1, length(address) - 3) WHERE address LIKE 'http://%:80';
