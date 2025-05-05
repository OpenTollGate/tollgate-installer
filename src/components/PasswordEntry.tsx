import React, { useState, useRef, useEffect } from 'react';
import { Html5Qrcode } from 'html5-qrcode';
import styled from 'styled-components';
import Button from './common/Button';
import Input from './common/Input';
import PageContainer from './common/PageContainer';
import { useNostrVersions } from './NostrVersionProvider';
import { NDKEvent } from '@nostr-dev-kit/ndk';

interface RouterInfo {
  ip: string;
  compatible?: boolean;
  boardName?: string;
  architecture?: string;
}


interface PasswordEntryProps {
  routerIp: string;
  password: string;
  setPassword: (password: string) => void;
  onSubmit: (version?: string) => void;
  error: string | null;
  onBack: () => void;
  router: RouterInfo | null;
}

const PasswordForm = styled.form`
  margin-top: 1rem;
`;

const QrScannerContainer = styled.div`
  margin-top: 1.5rem;
  padding: 1rem;
  background-color: #FFFFFF;
  border-radius: ${props => props.theme.radii.md};
`;

const ScannerTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.md};
  margin-bottom: 0.75rem;
  font-weight: ${props => props.theme.fontWeights.medium};
`;

const QrContainer = styled.div`
  width: 100%;
  max-width: 300px;
  height: 300px;
  margin: 0 auto;
  position: relative;
  overflow: hidden;
`;

const OrDivider = styled.div`
  display: flex;
  align-items: center;
  text-align: center;
  margin: 1.5rem 0;
  
  &::before,
  &::after {
    content: '';
    flex: 1;
    border-bottom: 1px solid ${props => props.theme.colors.border};
  }
  
  &::before {
    margin-right: 1rem;
  }
  
  &::after {
    margin-left: 1rem;
  }
`;

const ButtonGroup = styled.div`
  display: flex;
  gap: 1rem;
  margin-top: 1.5rem;
`;

const InfoText = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  font-size: ${props => props.theme.fontSizes.md};
  margin-bottom: 1.5rem;
`;

const ErrorMessage = styled.div`
  color: ${props => props.theme.colors.error};
  background-color: ${props => props.theme.colors.primaryLight};
  padding: 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1rem;
`;

const RouterInfo = styled.div`
  display: flex;
  align-items: center;
  padding: 0.75rem;
  background-color: ${props => props.theme.colors.backgroundAlt};
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1.5rem;
`;

const RouterIP = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  font-size: ${props => props.theme.fontSizes.md};
`;

const VersionSelectorContainer = styled.div`
  margin-bottom: 1rem;
  position: relative;
`;

const VersionSelectorButton = styled(Button)`
  width: 100%;
  justify-content: space-between;
  padding: 0.75rem 1rem;
`;

const VersionList = styled.ul`
  position: absolute;
  top: 100%;
  left: 0;
  width: 100%;
  background: white;
  border: 1px solid ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  list-style: none;
  padding: 0;
  margin: 0;
  z-index: 2;
  max-height: 200px;
  overflow-y: auto;
`;

interface VersionItemProps {
  $isSelected: boolean;
  $isCompatible: boolean;
}

const VersionItem = styled.li<VersionItemProps>`
  padding: 0.5rem 1rem;
  cursor: pointer;
  background-color: ${props => props.$isSelected ? props.theme.colors.primaryLight : 'transparent'};
  color: ${props => props.$isCompatible ? props.theme.colors.text : props.theme.colors.textSecondary};
  opacity: ${props => props.$isCompatible ? 1 : 0.6};
  
  &:hover {
    background-color: ${props => props.theme.colors.primaryLight};
  }
`;

const VersionText = styled.span`
  font-size: ${props => props.theme.fontSizes.md};
`;

const PasswordEntry: React.FC<PasswordEntryProps> = ({
  routerIp,
  password,
  setPassword,
  onSubmit,
  error,
  onBack,
  router
}) => {
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [showQrScanner, setShowQrScanner] = useState(false);
  const [showVersionSelector, setShowVersionSelector] = useState(false);
  const [selectedVersion, setSelectedVersion] = useState<string | null>(null);
  const qrScannerRef = useRef<Html5Qrcode | null>(null);
  const qrContainerRef = useRef<HTMLDivElement>(null);
  const { versions, loading, compatibleVersions } = useNostrVersions();

  useEffect(() => {
    if (showQrScanner && qrContainerRef.current) {
      const qrScannerId = "qr-scanner-container";
      
      if (!document.getElementById(qrScannerId)) {
        const container = document.createElement("div");
        container.id = qrScannerId;
        qrContainerRef.current.appendChild(container);
      }

      qrScannerRef.current = new Html5Qrcode(qrScannerId);
      
      qrScannerRef.current.start(
        { facingMode: "environment" },
        {
          fps: 10,
          qrbox: 250
        },
        (decodedText) => {
          setPassword(decodedText);
          stopQrScanner();
          handleSubmit();
        },
        (errorMessage) => {
          console.log(errorMessage);
        }
      )
      .catch(err => {
        console.error("Error starting QR scanner:", err);
      });
    }

    return () => {
      stopQrScanner();
    };
  }, [showQrScanner]);

  const stopQrScanner = () => {
    if (qrScannerRef.current && qrScannerRef.current.isScanning) {
      qrScannerRef.current.stop()
        .then(() => {
          setShowQrScanner(false);
        })
        .catch(err => {
          console.error("Error stopping QR scanner:", err);
        });
    }
  };

  const toggleQrScanner = () => {
    setShowQrScanner(!showQrScanner);
  };

  const handleSubmit = (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    setIsSubmitting(true);
    onSubmit(selectedVersion || undefined);
    setTimeout(() => {
      setIsSubmitting(false);
    }, 2000);
  };

  const handleVersionSelect = (version: string) => {
    setSelectedVersion(version);
    setShowVersionSelector(false);
  };

  return (
    <PageContainer 
      title="Router Password" 
      subtitle="Enter the password for your router"
    >
      {error && <ErrorMessage>{error}</ErrorMessage>}
      
      <RouterInfo>
        <RouterIP>IP Address: {routerIp}</RouterIP>
      </RouterInfo>
      
      <InfoText>
        Enter the root password for your router. This is typically the admin password
        you use to access the router's settings page.
      </InfoText>
      
      <VersionSelectorContainer>
        <VersionSelectorButton
          variant="outline"
          onClick={() => setShowVersionSelector(!showVersionSelector)}
        >
          <span>{selectedVersion || 'Select TollGateOS Version'}</span>
          <span>▼</span>
        </VersionSelectorButton>
        {showVersionSelector && (
          <VersionList>
            {compatibleVersions.map((version: NDKEvent) => (
              <VersionItem
                key={version.id}
                onClick={() => handleVersionSelect(version.id)}
                $isSelected={selectedVersion === version.id}
                $isCompatible={true}
              >
                <VersionText>
                  {version.id.substring(0, 8)} (Compatible)
                </VersionText>
              </VersionItem>
            ))}
            {versions
              .filter((version: NDKEvent) => !compatibleVersions.some(v => v.id === version.id))
              .map((version: NDKEvent) => (
                <VersionItem
                  key={version.id}
                  onClick={() => handleVersionSelect(version.id)}
                  $isSelected={selectedVersion === version.id}
                  $isCompatible={compatibleVersions.some(v => v.id === version.id)}
                >
                  <VersionText>
                    {version.id.substring(0, 8)} (Incompatible)
                  </VersionText>
                </VersionItem>
              ))}
          </VersionList>
        )}
      </VersionSelectorContainer>

      {!showQrScanner ? (
        <>
          <PasswordForm onSubmit={(e) => handleSubmit(e)}>
            <Input
              label="Password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              fullWidth
              autoFocus
              placeholder="Enter router password"
              disabled={isSubmitting}
            />
          </PasswordForm>
          
          <OrDivider>OR</OrDivider>
          
          <Button
            variant="outline"
            fullWidth
            onClick={toggleQrScanner}
            disabled={isSubmitting}
          >
            Scan Router Password QR Code
          </Button>
        </>
      ) : (
        <QrScannerContainer>
          <ScannerTitle>Scan Router Password QR Code</ScannerTitle>
          <QrContainer ref={qrContainerRef} />
          <Button
            variant="outline"
            fullWidth
            onClick={stopQrScanner}
            style={{ marginTop: '1rem' }}
          >
            Cancel Scanning
          </Button>
        </QrScannerContainer>
      )}
      
      <ButtonGroup>
        <Button variant="outline" onClick={onBack} disabled={isSubmitting}>
          Back
        </Button>
        <Button
          variant="primary"
          type="submit"
          onClick={() => handleSubmit()}
          isLoading={isSubmitting}
          disabled={!selectedVersion}
        >
          Connect
        </Button>
      </ButtonGroup>
    </PageContainer>
  );
};

export default PasswordEntry;